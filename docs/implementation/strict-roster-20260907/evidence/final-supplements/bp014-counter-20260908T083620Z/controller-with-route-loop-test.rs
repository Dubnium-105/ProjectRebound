use super::relay::RelayClient;
use crate::api::{discovery::client_config::ClientConfig, multiplayer::connections};
use crate::vnt::types::SecretString;
use anyhow::{Context, Result, ensure};
use rand::RngCore;
use serde_json::{Value, json};
use std::collections::{HashMap, HashSet, VecDeque};
use std::io::ErrorKind;
use std::net::{IpAddr, Ipv4Addr, SocketAddr, SocketAddrV4, TcpStream, ToSocketAddrs, UdpSocket};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, mpsc};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};
use tungstenite::client::IntoClientRequest;
use tungstenite::http::HeaderValue;
use tungstenite::stream::MaybeTlsStream;
use tungstenite::{Message, WebSocket};

const PROBE_MAGIC: &[u8; 4] = b"RBPC";
const PROBE_REQUEST: u8 = 1;
const PROBE_RESPONSE: u8 = 2;
const PROBE_TIMEOUT: Duration = Duration::from_millis(700);
const REALTIME_QUEUE_CAPACITY: usize = 128;
const REALTIME_MAX_MESSAGE_BYTES: usize = 64 * 1024;

/// A full control queue terminates this carrier; silently dropping a scoped
/// route event could otherwise leave a stale path marked ready.
#[derive(Clone)]
struct RealtimeSender {
    sender: mpsc::SyncSender<Value>,
    stop: Arc<AtomicBool>,
}

impl RealtimeSender {
    fn send(&self, value: Value) -> std::result::Result<(), mpsc::TrySendError<Value>> {
        if value.to_string().len() > REALTIME_MAX_MESSAGE_BYTES {
            self.stop.store(true, Ordering::Release);
            return Err(mpsc::TrySendError::Full(value));
        }
        let result = self.sender.try_send(value);
        if result.is_err() {
            self.stop.store(true, Ordering::Release);
        }
        result
    }
}

fn realtime_channel(stop: Arc<AtomicBool>) -> (RealtimeSender, mpsc::Receiver<Value>) {
    let (sender, receiver) = mpsc::sync_channel(REALTIME_QUEUE_CAPACITY);
    (RealtimeSender { sender, stop }, receiver)
}

struct StopOnWorkerExit(Arc<AtomicBool>);
impl Drop for StopOnWorkerExit {
    fn drop(&mut self) {
        self.0.store(true, Ordering::Release);
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LegacyRole {
    Host,
    Member,
}

pub struct LegacyManager {
    session: Option<LegacySession>,
}

pub struct RealtimeFeed {
    stop: Arc<AtomicBool>,
    events: mpsc::Receiver<Value>,
    thread: Option<JoinHandle<()>>,
    _outgoing: RealtimeSender,
}

impl RealtimeFeed {
    pub fn connect(url: &str, token: &str) -> Result<Self> {
        let url = normalized_realtime_url(url)?;
        let epoch = crate::security::auth::session_epoch_for_token(token)
            .context("realtime player session changed")?;
        let stop = Arc::new(AtomicBool::new(false));
        let (outgoing_tx, outgoing_rx) = realtime_channel(stop.clone());
        let (event_tx, event_rx) = realtime_channel(stop.clone());
        let (ready_tx, ready_rx) = mpsc::channel();
        let thread_stop = stop.clone();
        let handle = thread::Builder::new()
            .name("RoomRealtime".to_string())
            .spawn(move || {
                websocket_loop(
                    url,
                    epoch,
                    outgoing_rx,
                    event_tx,
                    Some(ready_tx),
                    thread_stop,
                )
            })?;
        if let Err(error) = ready_rx.recv_timeout(Duration::from_secs(5)) {
            stop.store(true, Ordering::Release);
            let _ = handle.join();
            return Err(error).context("connect authenticated room realtime channel");
        }
        Ok(Self {
            stop,
            events: event_rx,
            thread: Some(handle),
            _outgoing: outgoing_tx,
        })
    }

    pub fn ensure_running(&self) -> Result<()> {
        ensure!(
            !self.stop.load(Ordering::Acquire),
            "REALTIME_CARRIER_STOPPED: reconnect queue overflow, authentication change, or worker exit"
        );
        Ok(())
    }

    pub fn try_recv(&self) -> Option<Value> {
        self.events.try_recv().ok()
    }
}

impl Drop for RealtimeFeed {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

struct LegacySession {
    target: String,
    stop: Arc<AtomicBool>,
    connection_ids: Arc<Mutex<HashSet<String>>>,
    route_status: Arc<Mutex<LegacyRouteStatus>>,
    snapshots: mpsc::SyncSender<connections::ConnectionData>,
    threads: Vec<JoinHandle<()>>,
    authority_endpoint: Arc<Mutex<Option<SocketAddr>>>,
}

#[derive(Default)]
struct LegacyRouteStatus {
    ready_connections: HashSet<String>,
    connection_errors: HashMap<String, String>,
    backend_error: Option<String>,
    transport_diagnostic: Option<String>,
    datagrams_received: u64,
    datagrams_sent: u64,
    packet_errors: HashMap<String, u64>,
}

#[derive(Default)]
struct PeerPath {
    candidates: HashMap<String, SocketAddr>,
    attempted: HashSet<String>,
    pending: Option<PendingProbe>,
    direct: Option<SocketAddr>,
    relay: Option<RelayClient>,
    selected_direct_path: Option<String>,
    control_plane_state: Option<String>,
    last_candidate_announcement: Option<Instant>,
    candidate_announcement_attempts: u8,
    game_channel: Option<UdpSocket>,
    allocation_id: Option<String>,
    migration: Option<RelayMigration>,
    pending_bind: Option<RelayBindTask>,
    relay_selected: bool,
}

struct RelayMigration {
    id: String,
    previous: String,
    next: Option<String>,
    bound: Option<RelayClient>,
    started: Instant,
    commit_requested: bool,
}

struct RelayBindTask {
    allocation_id: String,
    migration_id: Option<String>,
    result: mpsc::Receiver<Result<RelayClient>>,
    worker: Option<JoinHandle<()>>,
}

impl Drop for RelayBindTask {
    fn drop(&mut self) {
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

fn start_relay_bind(
    host: String,
    port: u16,
    token: SecretString,
    local_bind: Option<IpAddr>,
    allocation_id: String,
    migration_id: Option<String>,
) -> Result<RelayBindTask> {
    let (sender, result) = mpsc::sync_channel(1);
    let worker = thread::Builder::new()
        .name("RelayBind".into())
        .spawn(move || {
            let _ = sender.send(RelayClient::connect(&host, port, token, local_bind));
        })?;
    Ok(RelayBindTask {
        allocation_id,
        migration_id,
        result,
        worker: Some(worker),
    })
}

fn poll_relay_bind(peer: &mut PeerPath) -> Result<bool> {
    let Some(task) = peer.pending_bind.as_ref() else {
        return Ok(false);
    };
    let relay = match task.result.try_recv() {
        Ok(result) => result,
        Err(mpsc::TryRecvError::Empty) => return Ok(false),
        Err(mpsc::TryRecvError::Disconnected) => {
            Err(anyhow::anyhow!("relay bind worker disconnected"))
        }
    };
    let task = peer.pending_bind.take().unwrap();
    let relay = relay?;
    if let Some(id) = task.migration_id.as_deref() {
        let Some(migration) = peer
            .migration
            .as_mut()
            .filter(|migration| migration.id == id)
        else {
            return Ok(false);
        };
        migration.bound = Some(relay);
        if migration.commit_requested {
            let payload = json!({"migration_id":migration.id,"previous_allocation_id":migration.previous,"allocation_id":migration.next});
            return Ok(commit_relay_migration(peer, &payload));
        }
        return Ok(false);
    }
    peer.direct = None;
    peer.selected_direct_path = None;
    peer.allocation_id = Some(task.allocation_id.clone());
    peer.relay = Some(relay);
    Ok(peer.relay_selected)
}

struct PendingProbe {
    nonce: u64,
    path: String,
    remote: SocketAddr,
    started: Instant,
}

impl LegacyManager {
    pub fn new() -> Self {
        Self { session: None }
    }

    pub fn install_authority_endpoint(&mut self, endpoint: SocketAddr) -> Result<()> {
        ensure!(endpoint.port() != 0, "authority game port is zero");
        let session = self
            .session
            .as_ref()
            .context("no active Legacy transport")?;
        let mut current = session
            .authority_endpoint
            .lock()
            .map_err(|_| anyhow::anyhow!("authority endpoint lock poisoned"))?;
        let local = SocketAddr::from((Ipv4Addr::LOCALHOST, endpoint.port()));
        ensure!(
            current.is_none_or(|existing| existing == local),
            "live authority endpoint cannot change within an attempt"
        );
        *current = Some(local);
        Ok(())
    }

    pub fn start_host(
        &mut self,
        token: &str,
        room_id: &str,
        player_id: &str,
        config: &ClientConfig,
        local_bind: Option<IpAddr>,
    ) -> Result<String> {
        self.start(
            LegacyRole::Host,
            token,
            room_id,
            player_id,
            None,
            config,
            local_bind,
        )
    }

    pub fn start_member(
        &mut self,
        token: &str,
        room_id: &str,
        player_id: &str,
        connection_id: &str,
        config: &ClientConfig,
        local_bind: Option<IpAddr>,
    ) -> Result<String> {
        self.start(
            LegacyRole::Member,
            token,
            room_id,
            player_id,
            Some(connection_id.to_string()),
            config,
            local_bind,
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn start(
        &mut self,
        role: LegacyRole,
        token: &str,
        room_id: &str,
        player_id: &str,
        initial_connection: Option<String>,
        config: &ClientConfig,
        local_bind: Option<IpAddr>,
    ) -> Result<String> {
        let epoch = crate::security::auth::session_epoch_for_token(token)
            .context("transport player session changed")?;
        ensure!(
            self.session.is_none(),
            "Legacy room route is already active"
        );
        let game_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0))?;
        game_socket.set_nonblocking(true)?;
        let target = game_socket.local_addr()?.to_string();
        let network_socket = bind_network_socket(local_bind)?;
        let selected_local_bind = match network_socket.local_addr()?.ip() {
            ip if ip.is_unspecified() => None,
            ip => Some(ip),
        };
        let local_candidate = local_candidate(&network_socket, &config.stun_servers)?;
        let reflexive_candidate = discover_reflexive(&network_socket, &config.stun_servers).ok();
        network_socket.set_read_timeout(None)?;
        network_socket.set_nonblocking(true)?;

        let stop = Arc::new(AtomicBool::new(false));
        let connection_ids = Arc::new(Mutex::new(HashSet::new()));
        let route_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let authority_endpoint = Arc::new(Mutex::new(None));
        if let Some(connection_id) = initial_connection.as_ref()
            && let Ok(mut ids) = connection_ids.lock()
        {
            ids.insert(connection_id.clone());
        }
        let (outgoing_tx, outgoing_rx) = realtime_channel(stop.clone());
        let (event_tx, event_rx) = realtime_channel(stop.clone());
        let (ready_tx, ready_rx) = mpsc::channel();
        let ws_stop = stop.clone();
        let realtime_url = normalized_realtime_url(&config.realtime_url)?;
        let websocket_thread = thread::Builder::new()
            .name("LegacyRealtime".to_string())
            .spawn(move || {
                websocket_loop(
                    realtime_url,
                    epoch,
                    outgoing_rx,
                    event_tx,
                    Some(ready_tx),
                    ws_stop,
                )
            })?;
        if let Err(error) = ready_rx.recv_timeout(Duration::from_secs(5)) {
            stop.store(true, Ordering::Release);
            let _ = websocket_thread.join();
            return Err(error).context("connect authenticated room realtime channel");
        }
        // Realtime coordination is intentionally non-replaying. The host may
        // have published its candidate before a joining member finished the
        // authenticated WebSocket handshake, so merge an authoritative REST
        // snapshot with all events queued after that handshake.
        let initial_snapshot = if let Some(connection_id) = initial_connection.as_deref() {
            match connections::get(token, connection_id) {
                Ok(connection) => Some(connection),
                Err(error) => {
                    stop.store(true, Ordering::Release);
                    let _ = websocket_thread.join();
                    return Err(error).context("load initial Legacy connection candidates");
                }
            }
        } else {
            None
        };
        let (snapshot_tx, snapshot_rx) = mpsc::sync_channel(REALTIME_QUEUE_CAPACITY);

        let data_stop = stop.clone();
        let data_connections = connection_ids.clone();
        let data_route_status = route_status.clone();
        let data_authority_endpoint = authority_endpoint.clone();
        let room_id = room_id.to_string();
        let player_id = player_id.to_string();
        let data_thread = match thread::Builder::new()
            .name("LegacyRoomRoute".to_string())
            .spawn(move || {
                route_loop(
                    role,
                    room_id,
                    player_id,
                    game_socket,
                    network_socket,
                    local_candidate,
                    reflexive_candidate,
                    initial_connection,
                    initial_snapshot,
                    outgoing_tx,
                    event_rx,
                    snapshot_rx,
                    data_connections,
                    data_route_status,
                    data_stop,
                    selected_local_bind,
                    epoch,
                    data_authority_endpoint,
                )
            }) {
            Ok(thread) => thread,
            Err(error) => {
                stop.store(true, Ordering::Release);
                let _ = websocket_thread.join();
                return Err(error).context("spawn Legacy transport worker");
            }
        };
        self.session = Some(LegacySession {
            target: target.clone(),
            stop,
            connection_ids,
            route_status,
            snapshots: snapshot_tx,
            threads: vec![websocket_thread, data_thread],
            authority_endpoint,
        });
        Ok(target)
    }

    pub fn target(&self) -> Option<&str> {
        self.session.as_ref().map(|session| session.target.as_str())
    }

    /// Wait until the authoritative connection service and the local UDP
    /// route both agree that a usable carrier path exists.  Returning the
    /// loopback proxy target before this barrier lets Boundary begin travel
    /// into a black hole and was the source of the strict-roster guest crash.
    pub fn wait_for_connection(
        &self,
        access_token: &str,
        connection_id: &str,
        timeout: Duration,
    ) -> Result<String> {
        let session = self
            .session
            .as_ref()
            .context("Legacy room route is not active")?;
        ensure!(
            session
                .connection_ids
                .lock()
                .map(|ids| ids.contains(connection_id))
                .unwrap_or(false),
            "Legacy connection is not attached to the active route"
        );
        let deadline = Instant::now() + timeout;
        let mut last_state = "UNKNOWN".to_string();
        let mut candidate_count = 0_usize;
        let mut selected_path = None;
        loop {
            self.ensure_running()?;
            let (route_ready, route_error, backend_error, diagnostic) = session
                .route_status
                .lock()
                .map(|status| {
                    (
                        status.ready_connections.contains(connection_id),
                        status.connection_errors.get(connection_id).cloned(),
                        status.backend_error.clone(),
                        status.transport_diagnostic.clone(),
                    )
                })
                .unwrap_or_default();
            if let Some(error) = route_error.or(backend_error) {
                anyhow::bail!("Legacy carrier negotiation failed: {error}");
            }
            let request_error = match connections::get(access_token, connection_id) {
                Ok(connection) => {
                    last_state = connection.state.clone();
                    candidate_count = connection.candidates.len();
                    selected_path = connection.selected_path.clone();
                    session
                        .snapshots
                        .try_send(connection.clone())
                        .context("REALTIME_SNAPSHOT_BACKPRESSURE")?;
                    if connection.state == "CONNECTED" && route_ready {
                        return connection
                            .selected_path
                            .context("Connected Legacy carrier omitted its selected path");
                    }
                    if matches!(connection.state.as_str(), "FAILED" | "EXPIRED" | "CLOSED") {
                        let reason = connection
                            .failure_reason
                            .as_deref()
                            .unwrap_or("no failure reason");
                        anyhow::bail!(
                            "Legacy carrier entered terminal state {}: {}",
                            connection.state,
                            reason
                        );
                    }
                    None
                }
                Err(error) => Some(error.to_string()),
            };
            if Instant::now() >= deadline {
                let selected = selected_path.as_deref().unwrap_or("none");
                let diagnostic = diagnostic.as_deref().unwrap_or("none");
                let request_error = request_error.as_deref().unwrap_or("none");
                anyhow::bail!(
                    "Legacy carrier was not ready before timeout (state={last_state}, candidates={candidate_count}, selected_path={selected}, realtime={diagnostic}, control_plane={request_error})"
                );
            }
            thread::sleep(Duration::from_millis(200));
        }
    }

    pub fn ensure_running(&self) -> Result<()> {
        if let Some(session) = &self.session {
            ensure!(
                !session.stop.load(Ordering::Acquire),
                "REALTIME_CARRIER_STOPPED: transport requires cleanup and retry"
            );
        }
        Ok(())
    }

    pub fn stop(&mut self, access_token: &str) -> Result<()> {
        let Some(session) = self.session.as_mut() else {
            return Ok(());
        };
        session.stop.store(true, Ordering::Release);
        for thread in session.threads.drain(..) {
            let _ = thread.join();
        }
        let ids = session
            .connection_ids
            .lock()
            .map(|ids| ids.iter().cloned().collect::<Vec<_>>())
            .unwrap_or_default();
        let mut first_error = None;
        for connection_id in ids {
            match connections::close(access_token, &connection_id) {
                Ok(_) => {
                    if let Ok(mut ids) = session.connection_ids.lock() {
                        ids.remove(&connection_id);
                    }
                }
                Err(error) => {
                    if first_error.is_none() {
                        first_error = Some(error);
                    }
                }
            }
        }
        if let Some(error) = first_error {
            return Err(error)
                .context("cleanup_pending: transport connections require close retry");
        }
        self.session.take();
        Ok(())
    }
}

impl Default for LegacyManager {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for LegacyManager {
    fn drop(&mut self) {
        if let Some(mut session) = self.session.take() {
            session.stop.store(true, Ordering::Release);
            for thread in session.threads.drain(..) {
                let _ = thread.join();
            }
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn route_loop(
    role: LegacyRole,
    room_id: String,
    player_id: String,
    game_socket: UdpSocket,
    network_socket: UdpSocket,
    local_candidate: SocketAddr,
    reflexive_candidate: Option<SocketAddr>,
    initial_connection: Option<String>,
    initial_snapshot: Option<connections::ConnectionData>,
    outgoing: RealtimeSender,
    events: mpsc::Receiver<Value>,
    snapshots: mpsc::Receiver<connections::ConnectionData>,
    connection_ids: Arc<Mutex<HashSet<String>>>,
    route_status: Arc<Mutex<LegacyRouteStatus>>,
    stop: Arc<AtomicBool>,
    local_bind: Option<IpAddr>,
    epoch: u64,
    authority_endpoint: Arc<Mutex<Option<SocketAddr>>>,
) {
    let mut peers: HashMap<String, PeerPath> = HashMap::new();
    let mut game_peer = None;
    if let Some(connection_id) = initial_connection {
        if let Some(snapshot) = initial_snapshot.as_ref() {
            reconcile_connection_snapshot(
                role,
                &player_id,
                &network_socket,
                &route_status,
                &mut peers,
                snapshot,
            );
        }
        publish_candidates(
            &outgoing,
            &connection_id,
            local_candidate,
            reflexive_candidate,
        );
        note_candidate_announcement(peers.entry(connection_id).or_default(), Instant::now());
    }
    let mut buffer = [0_u8; 65_535];
    while !stop.load(Ordering::Acquire) {
        if role == LegacyRole::Host {
            game_peer = authority_endpoint
                .lock()
                .ok()
                .and_then(|endpoint| *endpoint);
        }
        while let Ok(snapshot) = snapshots.try_recv() {
            if snapshot.room_id == room_id
                && connection_ids
                    .lock()
                    .map(|ids| ids.contains(&snapshot.connection_id))
                    .unwrap_or(false)
            {
                reconcile_connection_snapshot(
                    role,
                    &player_id,
                    &network_socket,
                    &route_status,
                    &mut peers,
                    &snapshot,
                );
            }
        }
        while let Ok(event) = events.try_recv() {
            let lease = match crate::security::auth::ensure_access_token(Duration::from_secs(30)) {
                Ok(lease) if lease.session_epoch() == epoch => lease,
                _ => {
                    if let Ok(mut status) = route_status.lock() {
                        status.backend_error = Some("SESSION_CHANGED".into());
                    }
                    return;
                }
            };
            if event.get("type").and_then(Value::as_str) == Some("transport.resync_required") {
                let ids = connection_ids
                    .lock()
                    .map(|ids| ids.iter().cloned().collect::<Vec<_>>())
                    .unwrap_or_default();
                for id in ids {
                    match connections::get(lease.token(), &id) {
                        Ok(snapshot) if snapshot.room_id == room_id => {
                            reconcile_connection_snapshot(
                                role,
                                &player_id,
                                &network_socket,
                                &route_status,
                                &mut peers,
                                &snapshot,
                            );
                            publish_candidates(
                                &outgoing,
                                &id,
                                local_candidate,
                                reflexive_candidate,
                            );
                        }
                        _ => {
                            mark_route_error(
                                &route_status,
                                &id,
                                "REST resynchronization failed".into(),
                            );
                            peers.remove(&id);
                        }
                    }
                }
                continue;
            }
            if !reconcile_unknown_host_connection(
                role,
                &room_id,
                &player_id,
                lease.token(),
                &network_socket,
                &connection_ids,
                &route_status,
                &mut peers,
                &event,
            ) {
                continue;
            }
            handle_event(
                role,
                &room_id,
                &player_id,
                &network_socket,
                local_candidate,
                reflexive_candidate,
                &outgoing,
                &connection_ids,
                &route_status,
                &mut peers,
                event,
                local_bind,
            );
        }
        reannounce_member_candidates(
            role,
            &outgoing,
            local_candidate,
            reflexive_candidate,
            &mut peers,
            Instant::now(),
        );
        handle_network_datagrams(
            role,
            &network_socket,
            &game_socket,
            game_peer,
            &outgoing,
            &mut peers,
            &mut buffer,
            &route_status,
        );
        for (connection_id, peer) in &mut peers {
            match poll_relay_bind(peer) {
                Ok(true) => mark_route_ready(&route_status, connection_id),
                Ok(false) => {}
                Err(_) => {
                    peer.relay = None;
                    peer.migration = None;
                    mark_route_error(&route_status, connection_id, "RELAY_BIND_FAILED".into());
                }
            }
            if peer
                .migration
                .as_ref()
                .is_some_and(|migration| migration.started.elapsed() > Duration::from_secs(10))
            {
                peer.migration = None;
                peer.pending_bind = None;
                peer.relay = None;
                mark_route_error(
                    &route_status,
                    connection_id,
                    "RELAY_MIGRATION_TIMEOUT".into(),
                );
            }
            if role == LegacyRole::Host {
                advance_direct_probe(connection_id, peer, &network_socket, &outgoing);
            }
            if let Some(relay) = peer.relay.as_mut() {
                match relay.receive(&mut buffer) {
                    Ok(Some(count)) => record_packet_result(
                        &route_status,
                        false,
                        deliver_game_datagram(
                            role,
                            peer,
                            &game_socket,
                            game_peer,
                            &buffer[..count],
                        ),
                    ),
                    Ok(None) => {}
                    Err(error) => record_packet_result(&route_status, false, Err(error)),
                }
            }
            if let Some(relay) = peer
                .migration
                .as_mut()
                .and_then(|migration| migration.bound.as_mut())
            {
                let received = relay.receive(&mut buffer);
                match received {
                    Ok(Some(count)) => record_packet_result(
                        &route_status,
                        false,
                        deliver_game_datagram(
                            role,
                            peer,
                            &game_socket,
                            game_peer,
                            &buffer[..count],
                        ),
                    ),
                    Ok(None) => {}
                    Err(error) => record_packet_result(&route_status, false, Err(error)),
                }
            }
            if role == LegacyRole::Host {
                // Each remote owns one stable local UDP source. Native replies
                // return through that socket and are sent only to that peer.
                loop {
                    let received = match peer.game_channel.as_ref() {
                        Some(socket) => socket.recv(&mut buffer),
                        None => break,
                    };
                    match received {
                        Ok(count) => record_packet_result(
                            &route_status,
                            true,
                            send_peer_datagram(peer, &network_socket, &buffer[..count]),
                        ),
                        Err(error) if error.kind() == ErrorKind::WouldBlock => break,
                        Err(error) => {
                            record_packet_result(&route_status, true, Err(error.into()));
                            break;
                        }
                    }
                }
            }
        }
        if role == LegacyRole::Host {
            thread::sleep(Duration::from_millis(5));
            continue;
        }
        loop {
            match game_socket.recv_from(&mut buffer) {
                Ok((count, source)) => {
                    // The host endpoint is the locked listen socket on 7777;
                    // members learn exactly one local Boundary socket. Never
                    // let a second local UDP sender replace either endpoint.
                    if !accept_game_datagram_source(role, &mut game_peer, source) {
                        continue;
                    }
                    if peers.len() == 1
                        && let Some(peer) = peers.values_mut().next()
                    {
                        record_packet_result(
                            &route_status,
                            true,
                            send_peer_datagram(peer, &network_socket, &buffer[..count]),
                        );
                    }
                }
                Err(error) if error.kind() == ErrorKind::WouldBlock => break,
                Err(_) => break,
            }
        }
        thread::sleep(Duration::from_millis(5));
    }
}

fn deliver_game_datagram(
    role: LegacyRole,
    peer: &mut PeerPath,
    member_socket: &UdpSocket,
    endpoint: Option<SocketAddr>,
    payload: &[u8],
) -> Result<()> {
    let endpoint = endpoint.context("native game endpoint not installed")?;
    if role == LegacyRole::Host {
        if peer.game_channel.is_none() {
            let socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0))?;
            socket.connect(endpoint)?;
            socket.set_nonblocking(true)?;
            peer.game_channel = Some(socket);
        }
        peer.game_channel.as_ref().unwrap().send(payload)?;
    } else {
        member_socket.send_to(payload, endpoint)?;
    }
    Ok(())
}

fn send_peer_datagram(peer: &mut PeerPath, network: &UdpSocket, payload: &[u8]) -> Result<()> {
    if let Some(remote) = peer.direct {
        network.send_to(payload, remote)?;
    } else if let Some(relay) = peer.relay.as_mut() {
        relay.send(payload)?;
    } else {
        anyhow::bail!("peer carrier not ready");
    }
    Ok(())
}

fn record_packet_result(status: &Arc<Mutex<LegacyRouteStatus>>, sent: bool, result: Result<()>) {
    if let Ok(mut status) = status.lock() {
        match result {
            Ok(()) if sent => status.datagrams_sent = status.datagrams_sent.saturating_add(1),
            Ok(()) => status.datagrams_received = status.datagrams_received.saturating_add(1),
            Err(error) => {
                let code = error
                    .downcast_ref::<super::relay::RelayPacketError>()
                    .map(ToString::to_string)
                    .unwrap_or_else(|| "transport_io_or_not_ready".into());
                let count = status.packet_errors.entry(code.clone()).or_default();
                *count = count.saturating_add(1);
                status.transport_diagnostic = Some(code);
            }
        }
    }
}

fn accept_game_datagram_source(
    role: LegacyRole,
    game_peer: &mut Option<SocketAddr>,
    source: SocketAddr,
) -> bool {
    match *game_peer {
        Some(expected) => source == expected,
        None if role == LegacyRole::Member => {
            *game_peer = Some(source);
            true
        }
        None => false,
    }
}

#[allow(clippy::too_many_arguments)]
fn handle_event(
    role: LegacyRole,
    room_id: &str,
    player_id: &str,
    network_socket: &UdpSocket,
    local_candidate: SocketAddr,
    reflexive_candidate: Option<SocketAddr>,
    outgoing: &RealtimeSender,
    connection_ids: &Arc<Mutex<HashSet<String>>>,
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
    peers: &mut HashMap<String, PeerPath>,
    event: Value,
    local_bind: Option<IpAddr>,
) {
    let event_type = event
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let payload = event.get("payload").unwrap_or(&Value::Null);
    if event_type == "error" {
        let code = payload
            .get("code")
            .and_then(Value::as_str)
            .unwrap_or("REALTIME_ERROR");
        let message = payload
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("Realtime coordination rejected an event");
        if is_stale_candidate_reannouncement_error(code, message) {
            if let Ok(mut status) = route_status.lock() {
                status.transport_diagnostic = Some(
                    "stable candidate resynchronization completed after the connection advanced"
                        .to_string(),
                );
            }
            return;
        }
        if let Ok(mut status) = route_status.lock() {
            status.backend_error = Some(format!("{code}: {}", truncate_diagnostic(message)));
        }
        return;
    }
    if event_type == "legacy.transport_diagnostic" {
        let message = payload
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("realtime channel unavailable");
        if let Ok(mut status) = route_status.lock() {
            status.transport_diagnostic = Some(truncate_diagnostic(message));
        }
        return;
    }
    let connection_id = payload
        .get("connection_id")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if connection_id.is_empty() {
        return;
    }
    if event_type != "connection.created"
        && !connection_ids
            .lock()
            .is_ok_and(|ids| ids.contains(connection_id))
    {
        return;
    }
    match event_type {
        "connection.relay_migrating" => {
            let Some(peer) = peers.get_mut(connection_id) else {
                return;
            };
            let (Some(id), Some(previous)) = (
                payload.get("migration_id").and_then(Value::as_str),
                payload
                    .get("previous_allocation_id")
                    .and_then(Value::as_str),
            ) else {
                return;
            };
            if peer.allocation_id.as_deref() != Some(previous) || id.is_empty() {
                return;
            }
            match peer.migration.as_ref() {
                Some(current) if current.id == id => {}
                Some(_) => {
                    mark_route_error(
                        route_status,
                        connection_id,
                        "RELAY_MIGRATION_CONFLICT".into(),
                    );
                }
                None => {
                    peer.migration = Some(RelayMigration {
                        id: id.into(),
                        previous: previous.into(),
                        next: None,
                        bound: None,
                        started: Instant::now(),
                        commit_requested: false,
                    })
                }
            }
        }
        "connection.relay_migrated" => {
            let Some(peer) = peers.get_mut(connection_id) else {
                return;
            };
            if commit_relay_migration(peer, payload) {
                mark_route_ready(route_status, connection_id);
            }
        }
        "connection.created" => {
            if payload.get("room_id").and_then(Value::as_str) != Some(room_id) {
                return;
            }
            peers.entry(connection_id.to_string()).or_default();
            if let Ok(mut ids) = connection_ids.lock() {
                ids.insert(connection_id.to_string());
            }
            publish_candidates(
                outgoing,
                connection_id,
                local_candidate,
                reflexive_candidate,
            );
        }
        "connection.candidate" => {
            let candidate = payload.get("candidate").unwrap_or(&Value::Null);
            let Some((candidate_type, remote)) = parse_remote_candidate(
                candidate.get("player_id").and_then(Value::as_str),
                player_id,
                candidate.get("candidate_type").and_then(Value::as_str),
                candidate.get("protocol").and_then(Value::as_str),
                candidate.get("address").and_then(Value::as_str),
                candidate.get("port").and_then(Value::as_u64),
            ) else {
                return;
            };
            let peer = peers.entry(connection_id.to_string()).or_default();
            register_remote_candidate(
                role,
                network_socket,
                route_status,
                connection_id,
                peer,
                candidate_type,
                remote,
            );
            // Realtime does not replay. A member periodically republishes its
            // stable candidate until the host sees it; echoing the host's
            // stable candidate completes that bounded resynchronization and
            // also recovers a missed connection.created event.
            if role == LegacyRole::Host {
                publish_candidates(
                    outgoing,
                    connection_id,
                    local_candidate,
                    reflexive_candidate,
                );
            }
        }
        "connection.path_selected" => {
            let path = payload
                .get("selected_path")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if matches!(path, "LAN" | "IPV6" | "UDP_PUNCH") {
                let peer = peers.entry(connection_id.to_string()).or_default();
                install_selected_direct_path(route_status, connection_id, peer, path);
            } else if path == "UDP_RELAY" {
                let peer = peers.entry(connection_id.to_string()).or_default();
                peer.relay_selected = true;
                peer.selected_direct_path = None;
                if peer.relay.is_some() {
                    mark_route_ready(route_status, connection_id);
                } else if peer.pending_bind.is_none() {
                    mark_route_error(
                        route_status,
                        connection_id,
                        "selected UDP relay path was not installed locally".to_string(),
                    );
                }
            } else if !path.is_empty() {
                mark_route_error(
                    route_status,
                    connection_id,
                    format!("unsupported selected carrier path {path}"),
                );
            }
        }
        "connection.relay_allocated" => {
            let Some(allocation_id) = payload
                .get("allocation_id")
                .and_then(Value::as_str)
                .filter(|id| !id.is_empty())
            else {
                return;
            };
            let peer = peers.entry(connection_id.to_string()).or_default();
            if peer.allocation_id.as_deref() == Some(allocation_id) {
                return;
            }
            if peer.pending_bind.is_some() {
                return;
            }
            let migration_id = payload.get("migration_id").and_then(Value::as_str);
            if let Some(id) = migration_id {
                let previous = payload
                    .get("previous_allocation_id")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if peer.allocation_id.as_deref() != Some(previous) {
                    return;
                }
                if peer.migration.is_none() {
                    peer.migration = Some(RelayMigration {
                        id: id.into(),
                        previous: previous.into(),
                        next: None,
                        bound: None,
                        started: Instant::now(),
                        commit_requested: false,
                    });
                }
                let migration = peer.migration.as_ref().unwrap();
                if migration.id != id || migration.bound.is_some() {
                    return;
                }
            } else if peer.allocation_id.is_some() {
                return;
            }
            let relay = payload.get("relay").unwrap_or(&Value::Null);
            if relay.get("protocol").and_then(Value::as_str) != Some("UDP") {
                return;
            }
            let Some(host) = relay.get("host").and_then(Value::as_str) else {
                return;
            };
            let Some(port) = relay.get("port").and_then(Value::as_u64) else {
                return;
            };
            let Some(token) = payload.get("relay_token").and_then(Value::as_str) else {
                return;
            };
            let Ok(port) = u16::try_from(port) else {
                return;
            };
            if port == 0 {
                return;
            }
            match start_relay_bind(
                host.into(),
                port,
                SecretString::new(token),
                local_bind,
                allocation_id.into(),
                migration_id.map(str::to_owned),
            ) {
                Ok(task) => {
                    if let Some(migration) = peer.migration.as_mut() {
                        migration.next = Some(allocation_id.into());
                    }
                    peer.pending_bind = Some(task);
                }
                Err(_) => {
                    mark_route_error(
                        route_status,
                        connection_id,
                        "failed to install allocated UDP relay path".to_string(),
                    );
                }
            }
        }
        "connection.relay_failed" | "connection.closed" => {
            if let Some(id) = payload.get("migration_id").and_then(Value::as_str) {
                if peers
                    .get(connection_id)
                    .and_then(|peer| peer.migration.as_ref())
                    .is_none_or(|migration| migration.id != id)
                {
                    return;
                }
            }
            let reason = payload
                .get("code")
                .or_else(|| payload.get("failure_reason"))
                .and_then(Value::as_str)
                .unwrap_or(event_type);
            mark_route_error(route_status, connection_id, truncate_diagnostic(reason));
            peers.remove(connection_id);
            if let Ok(mut ids) = connection_ids.lock() {
                ids.remove(connection_id);
            }
        }
        _ => {}
    }
}

fn commit_relay_migration(peer: &mut PeerPath, payload: &Value) -> bool {
    let Some(pending) = peer.migration.as_mut() else {
        return false;
    };
    if payload.get("migration_id").and_then(Value::as_str) != Some(pending.id.as_str())
        || payload
            .get("previous_allocation_id")
            .and_then(Value::as_str)
            != Some(pending.previous.as_str())
        || payload.get("allocation_id").and_then(Value::as_str) != pending.next.as_deref()
    {
        return false;
    }
    if pending.bound.is_none() {
        pending.commit_requested = true;
        return false;
    }
    let pending = peer.migration.take().unwrap();
    peer.relay = pending.bound;
    peer.allocation_id = pending.next;
    peer.direct = None;
    peer.selected_direct_path = None;
    true
}

#[allow(clippy::too_many_arguments)]
fn reconcile_unknown_host_connection(
    role: LegacyRole,
    room_id: &str,
    player_id: &str,
    access_token: &str,
    network_socket: &UdpSocket,
    connection_ids: &Arc<Mutex<HashSet<String>>>,
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
    peers: &mut HashMap<String, PeerPath>,
    event: &Value,
) -> bool {
    if role != LegacyRole::Host
        || event.get("type").and_then(Value::as_str) != Some("connection.candidate")
    {
        return true;
    }
    let Some(connection_id) = event
        .get("payload")
        .and_then(|payload| payload.get("connection_id"))
        .and_then(Value::as_str)
    else {
        return false;
    };
    if connection_ids
        .lock()
        .map(|ids| ids.contains(connection_id))
        .unwrap_or(false)
    {
        return true;
    }
    let Ok(snapshot) = connections::get(access_token, connection_id) else {
        return false;
    };
    if snapshot.room_id != room_id
        || snapshot.host_player_id != player_id
        || snapshot.connection_id != connection_id
    {
        return false;
    }
    if let Ok(mut ids) = connection_ids.lock() {
        ids.insert(connection_id.to_string());
    }
    reconcile_connection_snapshot(
        role,
        player_id,
        network_socket,
        route_status,
        peers,
        &snapshot,
    );
    true
}

fn reconcile_connection_snapshot(
    role: LegacyRole,
    player_id: &str,
    network_socket: &UdpSocket,
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
    peers: &mut HashMap<String, PeerPath>,
    connection: &connections::ConnectionData,
) {
    let connection_id = connection.connection_id.as_str();
    let remote_candidates = remote_candidates_from_snapshot(connection, player_id);
    let peer = peers.entry(connection_id.to_string()).or_default();
    peer.control_plane_state = Some(connection.state.clone());
    for (candidate_type, remote) in remote_candidates {
        register_remote_candidate(
            role,
            network_socket,
            route_status,
            connection_id,
            peer,
            candidate_type,
            remote,
        );
    }
    match connection.selected_path.as_deref() {
        Some(path @ ("LAN" | "IPV6" | "UDP_PUNCH")) => {
            install_selected_direct_path(route_status, connection_id, peer, path);
        }
        Some("UDP_RELAY") if peer.relay.is_some() => mark_route_ready(route_status, connection_id),
        _ => {}
    }
}

fn register_remote_candidate(
    role: LegacyRole,
    network_socket: &UdpSocket,
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
    connection_id: &str,
    peer: &mut PeerPath,
    candidate_type: String,
    remote: SocketAddr,
) {
    let changed = peer.candidates.insert(candidate_type.clone(), remote) != Some(remote);
    if selected_candidate_type(peer.selected_direct_path.as_deref())
        == Some(candidate_type.as_str())
    {
        peer.direct = Some(remote);
        peer.pending = None;
        mark_route_ready(route_status, connection_id);
    }
    // A SRFLX candidate recovered from REST must prime the member NAT just as
    // a live candidate event does. Repeated snapshots do not resend probes.
    if changed && role == LegacyRole::Member && candidate_type == "SRFLX" {
        let nonce = rand::random::<u64>();
        let _ = network_socket.send_to(&probe_packet(PROBE_REQUEST, nonce), remote);
    }
}

fn install_selected_direct_path(
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
    connection_id: &str,
    peer: &mut PeerPath,
    path: &str,
) {
    peer.selected_direct_path = Some(path.to_string());
    peer.pending = None;
    let verified_direct = peer.direct.filter(|_| peer.attempted.contains(path));
    peer.direct = verified_direct.or_else(|| {
        selected_candidate_type(Some(path))
            .and_then(|candidate_type| peer.candidates.get(candidate_type).copied())
    });
    if peer.direct.is_some() {
        mark_route_ready(route_status, connection_id);
    }
}

fn reannounce_member_candidates(
    role: LegacyRole,
    outgoing: &RealtimeSender,
    local_candidate: SocketAddr,
    reflexive_candidate: Option<SocketAddr>,
    peers: &mut HashMap<String, PeerPath>,
    now: Instant,
) {
    if role != LegacyRole::Member {
        return;
    }
    for (connection_id, peer) in peers {
        if !should_reannounce_member_candidates(peer, now) {
            continue;
        }
        publish_candidates(
            outgoing,
            connection_id,
            local_candidate,
            reflexive_candidate,
        );
        note_candidate_announcement(peer, now);
    }
}

fn should_reannounce_member_candidates(peer: &PeerPath, now: Instant) -> bool {
    if peer.direct.is_some() || peer.relay.is_some() {
        return false;
    }
    if peer.control_plane_state.as_deref().is_some_and(|state| {
        !matches!(
            state,
            "CREATED" | "GATHERING_CANDIDATES" | "CHECKING_DIRECT"
        )
    }) {
        return false;
    }
    let Some(last) = peer.last_candidate_announcement else {
        return true;
    };
    now.saturating_duration_since(last)
        >= candidate_reannouncement_delay(peer.candidate_announcement_attempts)
}

fn candidate_reannouncement_delay(attempts: u8) -> Duration {
    match attempts {
        0 => Duration::ZERO,
        1 => Duration::from_millis(500),
        2 => Duration::from_secs(1),
        3 => Duration::from_secs(2),
        _ => Duration::from_secs(4),
    }
}

fn note_candidate_announcement(peer: &mut PeerPath, now: Instant) {
    peer.last_candidate_announcement = Some(now);
    peer.candidate_announcement_attempts = peer.candidate_announcement_attempts.saturating_add(1);
}

fn selected_candidate_type(path: Option<&str>) -> Option<&'static str> {
    match path? {
        "LAN" => Some("LAN"),
        "IPV6" => Some("IPV6"),
        "UDP_PUNCH" => Some("SRFLX"),
        _ => None,
    }
}

fn parse_remote_candidate(
    candidate_player_id: Option<&str>,
    local_player_id: &str,
    candidate_type: Option<&str>,
    protocol: Option<&str>,
    address: Option<&str>,
    port: Option<u64>,
) -> Option<(String, SocketAddr)> {
    if candidate_player_id == Some(local_player_id) || protocol != Some("UDP") {
        return None;
    }
    let candidate_type = candidate_type?;
    if !matches!(candidate_type, "LAN" | "IPV6" | "SRFLX") {
        return None;
    }
    let address = address?.parse::<IpAddr>().ok()?;
    let port = u16::try_from(port?).ok()?;
    Some((candidate_type.to_string(), SocketAddr::new(address, port)))
}

fn remote_candidates_from_snapshot(
    connection: &connections::ConnectionData,
    local_player_id: &str,
) -> HashMap<String, SocketAddr> {
    connection
        .candidates
        .iter()
        .filter_map(|candidate| {
            parse_remote_candidate(
                Some(&candidate.player_id),
                local_player_id,
                Some(&candidate.candidate_type),
                Some(&candidate.protocol),
                Some(&candidate.address),
                Some(u64::from(candidate.port)),
            )
        })
        .collect()
}

fn mark_route_ready(status: &Arc<Mutex<LegacyRouteStatus>>, connection_id: &str) {
    if let Ok(mut status) = status.lock() {
        status.connection_errors.remove(connection_id);
        status.ready_connections.insert(connection_id.to_string());
    }
}

fn mark_route_error(status: &Arc<Mutex<LegacyRouteStatus>>, connection_id: &str, message: String) {
    if let Ok(mut status) = status.lock() {
        status.ready_connections.remove(connection_id);
        status
            .connection_errors
            .insert(connection_id.to_string(), truncate_diagnostic(&message));
    }
}

fn truncate_diagnostic(value: &str) -> String {
    value.trim().chars().take(256).collect()
}

fn is_stale_candidate_reannouncement_error(code: &str, message: &str) -> bool {
    code == "INVALID_CONNECTION_STATE"
        && message == "Connection is not gathering direct candidates."
}

fn handle_network_datagrams(
    role: LegacyRole,
    network_socket: &UdpSocket,
    game_socket: &UdpSocket,
    game_peer: Option<SocketAddr>,
    outgoing: &RealtimeSender,
    peers: &mut HashMap<String, PeerPath>,
    buffer: &mut [u8],
    route_status: &Arc<Mutex<LegacyRouteStatus>>,
) {
    loop {
        match network_socket.recv_from(buffer) {
            Ok((count, source)) if count == 13 && &buffer[..4] == PROBE_MAGIC => {
                let nonce = u64::from_be_bytes(buffer[5..13].try_into().unwrap());
                if buffer[4] == PROBE_REQUEST {
                    let _ = network_socket.send_to(&probe_packet(PROBE_RESPONSE, nonce), source);
                } else if buffer[4] == PROBE_RESPONSE {
                    for (connection_id, peer) in peers.iter_mut() {
                        if peer.pending.as_ref().is_some_and(|pending| {
                            pending.nonce == nonce && pending.remote == source
                        }) {
                            let pending = peer.pending.take().unwrap();
                            peer.direct = Some(source);
                            peer.attempted.insert(pending.path.clone());
                            send_check_result(
                                outgoing,
                                connection_id,
                                true,
                                &pending.path,
                                pending.started.elapsed().as_millis() as u64,
                                "",
                            );
                            break;
                        }
                    }
                }
            }
            Ok((count, source)) => {
                let mut matching = peers
                    .values_mut()
                    .filter(|peer| peer.direct == Some(source));
                if let Some(peer) = matching.next()
                    && matching.next().is_none()
                {
                    record_packet_result(
                        route_status,
                        false,
                        deliver_game_datagram(role, peer, game_socket, game_peer, &buffer[..count]),
                    );
                }
            }
            Err(error) if error.kind() == ErrorKind::WouldBlock => break,
            Err(_) => break,
        }
    }
}

fn advance_direct_probe(
    connection_id: &str,
    peer: &mut PeerPath,
    network_socket: &UdpSocket,
    outgoing: &RealtimeSender,
) {
    if peer.direct.is_some() || peer.relay.is_some() {
        return;
    }
    if let Some(pending) = peer.pending.as_ref() {
        if pending.started.elapsed() < PROBE_TIMEOUT {
            return;
        }
        let pending = peer.pending.take().unwrap();
        peer.attempted.insert(pending.path.clone());
        send_check_result(
            outgoing,
            connection_id,
            false,
            &pending.path,
            PROBE_TIMEOUT.as_millis() as u64,
            "bounded direct probe timed out",
        );
        return;
    }
    for (candidate_type, path) in [("LAN", "LAN"), ("IPV6", "IPV6"), ("SRFLX", "UDP_PUNCH")] {
        if peer.attempted.contains(path) {
            continue;
        }
        let Some(remote) = peer.candidates.get(candidate_type).copied() else {
            continue;
        };
        let nonce = rand::random::<u64>();
        let _ = network_socket.send_to(&probe_packet(PROBE_REQUEST, nonce), remote);
        peer.pending = Some(PendingProbe {
            nonce,
            path: path.to_string(),
            remote,
            started: Instant::now(),
        });
        return;
    }
}

fn publish_candidates(
    outgoing: &RealtimeSender,
    connection_id: &str,
    local: SocketAddr,
    reflexive: Option<SocketAddr>,
) {
    let local_type = if local.ip().is_ipv6() { "IPV6" } else { "LAN" };
    let _ = outgoing.send(candidate_event(connection_id, local_type, local, 2_000_000));
    if let Some(reflexive) = reflexive.filter(|reflexive| *reflexive != local) {
        let _ = outgoing.send(candidate_event(
            connection_id,
            "SRFLX",
            reflexive,
            1_000_000,
        ));
    }
}

fn candidate_event(
    connection_id: &str,
    candidate_type: &str,
    address: SocketAddr,
    priority: i32,
) -> Value {
    json!({
        "type": "connection.candidate",
        "payload": {
            "connection_id": connection_id,
            "foundation": format!("toolbox-{}", candidate_type.to_ascii_lowercase()),
            "candidate_type": candidate_type,
            "protocol": "UDP",
            "address": address.ip().to_string(),
            "port": address.port(),
            "priority": priority
        }
    })
}

fn send_check_result(
    outgoing: &RealtimeSender,
    connection_id: &str,
    success: bool,
    path: &str,
    latency_ms: u64,
    reason: &str,
) {
    let _ = outgoing.send(json!({
        "type": "connection.check_result",
        "payload": {
            "connection_id": connection_id,
            "success": success,
            "path": path,
            "latency_ms": latency_ms.min(60_000),
            "reason": reason
        }
    }));
}

fn probe_packet(kind: u8, nonce: u64) -> [u8; 13] {
    let mut packet = [0_u8; 13];
    packet[..4].copy_from_slice(PROBE_MAGIC);
    packet[4] = kind;
    packet[5..].copy_from_slice(&nonce.to_be_bytes());
    packet
}

fn websocket_loop(
    url: String,
    epoch: u64,
    outgoing: mpsc::Receiver<Value>,
    events: RealtimeSender,
    ready: Option<mpsc::Sender<()>>,
    stop: Arc<AtomicBool>,
) {
    websocket_loop_with_provider(
        url,
        move || {
            let lease = crate::security::auth::ensure_access_token(Duration::from_secs(30))?;
            ensure!(lease.session_epoch() == epoch, "realtime session changed");
            Ok(SecretString::new(lease.token()))
        },
        outgoing,
        events,
        ready,
        stop,
    );
}

fn websocket_loop_with_provider(
    url: String,
    mut credentials: impl FnMut() -> Result<SecretString>,
    outgoing: mpsc::Receiver<Value>,
    events: RealtimeSender,
    mut ready: Option<mpsc::Sender<()>>,
    stop: Arc<AtomicBool>,
) {
    let _exit_guard = StopOnWorkerExit(stop.clone());
    let mut backoff = Duration::from_millis(250);
    let mut pending = VecDeque::new();
    let mut connected_once = false;
    while !stop.load(Ordering::Acquire) {
        let token = match credentials() {
            Ok(token) => token,
            Err(_) => {
                send_transport_diagnostic(
                    &events,
                    "realtime authentication expired or session changed",
                );
                return;
            }
        };
        match connect_websocket(&url, token.expose()) {
            Ok(mut socket) => {
                if connected_once {
                    pending.clear();
                    while outgoing.try_recv().is_ok() {}
                    let _ = events.send(json!({"type":"transport.resync_required","payload":{}}));
                }
                connected_once = true;
                let mut checked_session = Instant::now();
                if let Some(ready) = ready.take() {
                    let _ = ready.send(());
                }
                backoff = Duration::from_millis(250);
                while !stop.load(Ordering::Acquire) {
                    if checked_session.elapsed() >= Duration::from_secs(1) {
                        if credentials().is_err() {
                            return;
                        }
                        checked_session = Instant::now();
                    }
                    while let Ok(message) = outgoing.try_recv() {
                        if pending.len() >= REALTIME_QUEUE_CAPACITY {
                            send_transport_diagnostic(
                                &events,
                                "realtime queue exceeded its bounded capacity",
                            );
                            return;
                        }
                        pending.push_back(message);
                    }
                    while let Some(message) = pending.front() {
                        if socket
                            .send(Message::Text(message.to_string().into()))
                            .is_err()
                        {
                            send_transport_diagnostic(
                                &events,
                                "realtime send failed; reconnecting",
                            );
                            break;
                        }
                        pending.pop_front();
                    }
                    match socket.read() {
                        Ok(Message::Text(text)) => {
                            if let Ok(value) = serde_json::from_str::<Value>(&text) {
                                let _ = events.send(value);
                            }
                        }
                        Ok(Message::Close(_)) => break,
                        Err(tungstenite::Error::Capacity(_)) => {
                            stop.store(true, Ordering::Release);
                            return;
                        }
                        Ok(_) => {}
                        Err(tungstenite::Error::Io(error))
                            if matches!(
                                error.kind(),
                                ErrorKind::WouldBlock | ErrorKind::TimedOut
                            ) => {}
                        Err(_) => {
                            send_transport_diagnostic(
                                &events,
                                "realtime channel disconnected; reconnecting",
                            );
                            break;
                        }
                    }
                }
                let _ = socket.close(None);
            }
            Err(_) => {
                send_transport_diagnostic(&events, "realtime connection unavailable; retrying")
            }
        }
        if !stop.load(Ordering::Acquire) {
            let until = Instant::now() + backoff;
            while !stop.load(Ordering::Acquire) && Instant::now() < until {
                thread::sleep(Duration::from_millis(25));
            }
            backoff = (backoff * 2).min(Duration::from_secs(3));
        }
    }
}

fn send_transport_diagnostic(events: &RealtimeSender, message: &str) {
    let _ = events.send(json!({
        "type": "legacy.transport_diagnostic",
        "payload": { "message": message }
    }));
}

fn connect_websocket(url: &str, token: &str) -> Result<WebSocket<MaybeTlsStream<TcpStream>>> {
    let mut request = url.into_client_request()?;
    request.headers_mut().insert(
        tungstenite::http::header::AUTHORIZATION,
        HeaderValue::from_str(&format!("Bearer {token}"))?,
    );
    let parsed = reqwest::Url::parse(url)?;
    let host = parsed
        .host_str()
        .context("realtime host missing")?
        .trim_start_matches('[')
        .trim_end_matches(']');
    let port = parsed
        .port_or_known_default()
        .context("realtime port missing")?;
    let deadline = Instant::now() + Duration::from_secs(3);
    let mut connected = None;
    for address in (host, port).to_socket_addrs()? {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            break;
        }
        if let Ok(stream) = TcpStream::connect_timeout(&address, remaining) {
            connected = Some(stream);
            break;
        }
    }
    let stream = connected.context("realtime TCP connect deadline exceeded")?;
    stream.set_read_timeout(Some(Duration::from_secs(2)))?;
    stream.set_write_timeout(Some(Duration::from_secs(2)))?;
    let websocket_config = tungstenite::protocol::WebSocketConfig::default()
        .max_message_size(Some(REALTIME_MAX_MESSAGE_BYTES))
        .max_frame_size(Some(REALTIME_MAX_MESSAGE_BYTES));
    let (mut socket, _) =
        tungstenite::client_tls_with_config(request, stream, Some(websocket_config), None)
            .map_err(|_| anyhow::anyhow!("realtime TLS/WebSocket handshake failed"))?;
    set_websocket_timeout(socket.get_mut(), Some(Duration::from_millis(200)))?;
    Ok(socket)
}

fn set_websocket_timeout(
    stream: &mut MaybeTlsStream<TcpStream>,
    timeout: Option<Duration>,
) -> Result<()> {
    match stream {
        MaybeTlsStream::Plain(stream) => stream.set_read_timeout(timeout)?,
        MaybeTlsStream::NativeTls(stream) => stream.get_ref().set_read_timeout(timeout)?,
        _ => anyhow::bail!("unsupported WebSocket TLS backend"),
    }
    Ok(())
}

fn normalized_realtime_url(configured: &str) -> Result<String> {
    let value = configured.trim();
    #[cfg(feature = "lab-testing")]
    if crate::util::region::lab_api_origin_active()
        && crate::util::region::valid_lab_realtime_url(value)
    {
        return Ok(value.to_string());
    }
    ensure!(value.starts_with("wss://"), "Realtime URL must use wss://");
    Ok(value.to_string())
}

fn bind_network_socket(local_bind: Option<IpAddr>) -> Result<UdpSocket> {
    match local_bind {
        Some(IpAddr::V4(ip)) => UdpSocket::bind((ip, 0)).map_err(Into::into),
        Some(IpAddr::V6(_)) => {
            anyhow::bail!("Legacy room transport requires an IPv4 local interface")
        }
        None => UdpSocket::bind((Ipv4Addr::UNSPECIFIED, 0)).map_err(Into::into),
    }
}

fn local_candidate(socket: &UdpSocket, stun_servers: &[String]) -> Result<SocketAddr> {
    let bound_ip = socket.local_addr()?.ip();
    let ip = if bound_ip.is_unspecified() {
        let destination = stun_servers
            .iter()
            .find_map(|server| resolve_stun(server).ok())
            .unwrap_or_else(|| "8.8.8.8:53".parse().unwrap());
        let probe = UdpSocket::bind((Ipv4Addr::UNSPECIFIED, 0))?;
        probe.connect(destination)?;
        probe.local_addr()?.ip()
    } else {
        bound_ip
    };
    ensure!(
        ip.is_ipv4() && !ip.is_loopback() && !ip.is_unspecified(),
        "no usable local IPv4 address"
    );
    Ok(SocketAddr::new(ip, socket.local_addr()?.port()))
}

fn discover_reflexive(socket: &UdpSocket, stun_servers: &[String]) -> Result<SocketAddr> {
    let server = stun_servers
        .iter()
        .find_map(|server| resolve_stun(server).ok())
        .context("no STUN server is configured")?;
    let mut transaction = [0_u8; 12];
    rand::thread_rng().fill_bytes(&mut transaction);
    let mut request = [0_u8; 20];
    request[..2].copy_from_slice(&1_u16.to_be_bytes());
    request[4..8].copy_from_slice(&0x2112_A442_u32.to_be_bytes());
    request[8..].copy_from_slice(&transaction);
    socket.set_read_timeout(Some(Duration::from_millis(800)))?;
    socket.send_to(&request, server)?;
    let mut response = [0_u8; 1024];
    let (count, source) = socket.recv_from(&mut response)?;
    ensure!(
        source.ip() == server.ip() && count >= 20,
        "invalid STUN response source"
    );
    ensure!(
        response[..2] == 0x0101_u16.to_be_bytes()
            && response[4..8] == 0x2112_A442_u32.to_be_bytes()
            && response[8..20] == transaction,
        "invalid STUN binding response"
    );
    parse_stun_address(&response[..count]).context("STUN response omitted an IPv4 mapping")
}

fn parse_stun_address(response: &[u8]) -> Option<SocketAddr> {
    let mut offset = 20;
    while offset + 4 <= response.len() {
        let kind = u16::from_be_bytes(response[offset..offset + 2].try_into().ok()?);
        let length = u16::from_be_bytes(response[offset + 2..offset + 4].try_into().ok()?) as usize;
        let value = response.get(offset + 4..offset + 4 + length)?;
        if matches!(kind, 0x0001 | 0x0020) && value.len() >= 8 && value[1] == 1 {
            let mut port = u16::from_be_bytes(value[2..4].try_into().ok()?);
            let mut address = u32::from_be_bytes(value[4..8].try_into().ok()?);
            if kind == 0x0020 {
                port ^= 0x2112;
                address ^= 0x2112_A442;
            }
            return Some(SocketAddr::V4(SocketAddrV4::new(
                Ipv4Addr::from(address),
                port,
            )));
        }
        offset += 4 + (length + 3) / 4 * 4;
    }
    None
}

fn resolve_stun(value: &str) -> Result<SocketAddr> {
    let value = value.trim().strip_prefix("stun:").unwrap_or(value.trim());
    let value = if value.rsplit_once(':').is_some() {
        value.to_string()
    } else {
        format!("{value}:3478")
    };
    select_ipv4_address(value.to_socket_addrs()?).context("STUN server resolved to no IPv4 address")
}

fn select_ipv4_address(addresses: impl IntoIterator<Item = SocketAddr>) -> Option<SocketAddr> {
    addresses.into_iter().find(SocketAddr::is_ipv4)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn migration_rejects_old_completion_and_waits_for_local_bind() {
        let mut peer = PeerPath {
            allocation_id: Some("old-allocation".into()),
            migration: Some(RelayMigration {
                id: "migration-current".into(),
                previous: "old-allocation".into(),
                next: Some("new-allocation".into()),
                bound: None,
                started: Instant::now(),
                commit_requested: false,
            }),
            ..Default::default()
        };
        assert!(!commit_relay_migration(
            &mut peer,
            &json!({"migration_id":"migration-old","previous_allocation_id":"old-allocation","allocation_id":"new-allocation"})
        ));
        assert!(!peer.migration.as_ref().unwrap().commit_requested);
        assert!(!commit_relay_migration(
            &mut peer,
            &json!({"migration_id":"migration-current","previous_allocation_id":"old-allocation","allocation_id":"new-allocation"})
        ));
        assert!(peer.migration.as_ref().unwrap().commit_requested);
        assert_eq!(peer.allocation_id.as_deref(), Some("old-allocation"));
    }

    #[test]
    fn a_slow_relay_bind_does_not_block_other_peer_routing() {
        let silent_edge = UdpSocket::bind("127.0.0.1:0").unwrap();
        let mut peer = PeerPath::default();
        peer.pending_bind = Some(
            start_relay_bind(
                "127.0.0.1".into(),
                silent_edge.local_addr().unwrap().port(),
                SecretString::new("synthetic-bind"),
                None,
                "allocation".into(),
                None,
            )
            .unwrap(),
        );
        let started = Instant::now();
        assert!(!poll_relay_bind(&mut peer).unwrap());
        assert!(started.elapsed() < Duration::from_millis(250));
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut failed = false;
        while Instant::now() < deadline {
            if poll_relay_bind(&mut peer).is_err() {
                failed = true;
                break;
            }
            thread::sleep(Duration::from_millis(10));
        }
        assert!(failed);
        assert!(peer.pending_bind.is_none());
        assert!(peer.relay.is_none());
    }

    #[test]
    fn host_two_members_have_distinct_native_sources_and_unicast_replies() {
        let authority = UdpSocket::bind("127.0.0.1:0").unwrap();
        authority
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        let endpoint = authority.local_addr().unwrap();
        let unused = UdpSocket::bind("127.0.0.1:0").unwrap();
        let network = UdpSocket::bind("127.0.0.1:0").unwrap();
        let member_a = UdpSocket::bind("127.0.0.1:0").unwrap();
        let member_b = UdpSocket::bind("127.0.0.1:0").unwrap();
        member_a
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        member_b
            .set_read_timeout(Some(Duration::from_secs(1)))
            .unwrap();
        let mut a = PeerPath {
            direct: Some(member_a.local_addr().unwrap()),
            ..Default::default()
        };
        let mut b = PeerPath {
            direct: Some(member_b.local_addr().unwrap()),
            ..Default::default()
        };
        deliver_game_datagram(LegacyRole::Host, &mut a, &unused, Some(endpoint), b"A").unwrap();
        deliver_game_datagram(LegacyRole::Host, &mut b, &unused, Some(endpoint), b"B").unwrap();
        let mut data = [0; 16];
        let (_, source_a) = authority.recv_from(&mut data).unwrap();
        assert_eq!(data[0], b'A');
        let (_, source_b) = authority.recv_from(&mut data).unwrap();
        assert_eq!(data[0], b'B');
        assert_ne!(source_a, source_b);
        authority.send_to(b"reply-A", source_a).unwrap();
        authority.send_to(b"reply-B", source_b).unwrap();
        for peer in [&mut a, &mut b] {
            peer.game_channel
                .as_ref()
                .unwrap()
                .set_nonblocking(false)
                .unwrap();
            peer.game_channel
                .as_ref()
                .unwrap()
                .set_read_timeout(Some(Duration::from_secs(1)))
                .unwrap();
            let count = peer.game_channel.as_ref().unwrap().recv(&mut data).unwrap();
            send_peer_datagram(peer, &network, &data[..count]).unwrap();
        }
        let count = member_a.recv(&mut data).unwrap();
        assert_eq!(&data[..count], b"reply-A");
        let count = member_b.recv(&mut data).unwrap();
        assert_eq!(&data[..count], b"reply-B");
        drop(a);
        deliver_game_datagram(
            LegacyRole::Host,
            &mut b,
            &unused,
            Some(endpoint),
            b"still-B",
        )
        .unwrap();
        let (_, stable_b) = authority.recv_from(&mut data).unwrap();
        assert_eq!(stable_b, source_b);
    }

    #[test]
    fn probe_packets_are_fixed_and_non_secret() {
        let packet = probe_packet(PROBE_REQUEST, 42);
        assert_eq!(&packet[..4], PROBE_MAGIC);
        assert_eq!(u64::from_be_bytes(packet[5..].try_into().unwrap()), 42);
    }

    #[test]
    fn realtime_requires_tls() {
        assert!(normalized_realtime_url("wss://example.test/v1/realtime/connect").is_ok());
        assert!(normalized_realtime_url("ws://example.test/v1/realtime/connect").is_err());
    }

    #[test]
    fn ipv4_socket_ignores_an_ipv6_first_dns_result() {
        let ipv6 = "[2001:db8::1]:3478".parse().unwrap();
        let ipv4 = "192.0.2.10:3478".parse().unwrap();
        assert_eq!(select_ipv4_address([ipv6, ipv4]), Some(ipv4));
        assert_eq!(select_ipv4_address([ipv6]), None);
    }

    #[test]
    fn candidate_event_matches_control_plane_contract() {
        let value = candidate_event(
            "conn_test",
            "LAN",
            "192.168.10.20:40123".parse().unwrap(),
            2_000_000,
        );
        assert_eq!(value["type"], "connection.candidate");
        assert_eq!(value["payload"]["connection_id"], "conn_test");
        assert_eq!(value["payload"]["candidate_type"], "LAN");
        assert_eq!(value["payload"]["protocol"], "UDP");
        assert_eq!(value["payload"]["address"], "192.168.10.20");
        assert_eq!(value["payload"]["port"], 40123);
    }

    #[test]
    fn connection_snapshot_restores_only_usable_remote_candidates() {
        let candidate =
            |player_id: &str, candidate_type: &str, protocol: &str, address: &str, port: u16| {
                connections::ConnectionCandidate {
                    candidate_id: format!("candidate-{player_id}-{candidate_type}"),
                    connection_id: "conn_test".to_string(),
                    player_id: player_id.to_string(),
                    foundation: "toolbox".to_string(),
                    candidate_type: candidate_type.to_string(),
                    protocol: protocol.to_string(),
                    address: address.to_string(),
                    port,
                    priority: 1,
                    created_at: "2026-08-27T00:00:00Z".to_string(),
                }
            };
        let connection = connections::ConnectionData {
            connection_id: "conn_test".to_string(),
            room_id: "room_test".to_string(),
            host_player_id: "host".to_string(),
            peer_player_id: "member".to_string(),
            state: "CHECKING".to_string(),
            selected_path: None,
            failure_reason: None,
            expires_at: "2026-08-27T01:00:00Z".to_string(),
            created_at: "2026-08-27T00:00:00Z".to_string(),
            updated_at: "2026-08-27T00:00:00Z".to_string(),
            candidates: vec![
                candidate("member", "LAN", "UDP", "192.168.1.20", 41000),
                candidate("host", "LAN", "UDP", "192.168.1.10", 42000),
                candidate("host", "IPV6", "UDP", "2001:db8::10", 43000),
                candidate("host", "SRFLX", "TCP", "198.51.100.10", 44000),
            ],
        };

        let candidates = remote_candidates_from_snapshot(&connection, "member");

        assert_eq!(candidates.len(), 2);
        assert_eq!(
            candidates.get("LAN"),
            Some(&"192.168.1.10:42000".parse().unwrap())
        );
        assert_eq!(
            candidates.get("IPV6"),
            Some(&"[2001:db8::10]:43000".parse().unwrap())
        );
        assert!(!candidates.contains_key("SRFLX"));
    }

    #[test]
    fn selected_lan_path_waits_for_a_late_remote_candidate() {
        let network_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let local_candidate = network_socket.local_addr().unwrap();
        let (outgoing, _messages) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let connection_ids = Arc::new(Mutex::new(HashSet::from(["conn_test".to_string()])));
        let route_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let mut peers = HashMap::new();

        handle_event(
            LegacyRole::Member,
            "room_test",
            "member",
            &network_socket,
            local_candidate,
            None,
            &outgoing,
            &connection_ids,
            &route_status,
            &mut peers,
            json!({
                "type": "connection.path_selected",
                "payload": {
                    "connection_id": "conn_test",
                    "selected_path": "LAN"
                }
            }),
            None,
        );
        {
            let status = route_status.lock().unwrap();
            assert!(!status.ready_connections.contains("conn_test"));
            assert!(!status.connection_errors.contains_key("conn_test"));
        }

        handle_event(
            LegacyRole::Member,
            "room_test",
            "member",
            &network_socket,
            local_candidate,
            None,
            &outgoing,
            &connection_ids,
            &route_status,
            &mut peers,
            json!({
                "type": "connection.candidate",
                "payload": {
                    "connection_id": "conn_test",
                    "candidate": {
                        "player_id": "host",
                        "candidate_type": "LAN",
                        "protocol": "UDP",
                        "address": "192.168.1.10",
                        "port": 42000
                    }
                }
            }),
            None,
        );

        assert!(
            route_status
                .lock()
                .unwrap()
                .ready_connections
                .contains("conn_test")
        );
        assert_eq!(
            peers.get("conn_test").and_then(|peer| peer.direct),
            Some("192.168.1.10:42000".parse().unwrap())
        );
    }

    #[test]
    fn host_echoes_candidates_when_a_member_reannounces_after_realtime_loss() {
        let network_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let local_candidate: SocketAddr = "192.168.1.10:41000".parse().unwrap();
        let (outgoing, messages) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let connection_ids = Arc::new(Mutex::new(HashSet::from(["conn_test".to_string()])));
        let route_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let mut peers = HashMap::new();

        handle_event(
            LegacyRole::Host,
            "room_test",
            "host",
            &network_socket,
            local_candidate,
            None,
            &outgoing,
            &connection_ids,
            &route_status,
            &mut peers,
            json!({
                "type": "connection.candidate",
                "payload": {
                    "connection_id": "conn_test",
                    "candidate": {
                        "player_id": "member",
                        "candidate_type": "LAN",
                        "protocol": "UDP",
                        "address": "192.168.1.20",
                        "port": 42000
                    }
                }
            }),
            None,
        );

        let echoed = messages.try_recv().unwrap();
        assert_eq!(echoed["type"], "connection.candidate");
        assert_eq!(echoed["payload"]["connection_id"], "conn_test");
        assert_eq!(echoed["payload"]["address"], "192.168.1.10");
        assert_eq!(
            peers
                .get("conn_test")
                .and_then(|peer| peer.candidates.get("LAN")),
            Some(&"192.168.1.20:42000".parse().unwrap())
        );
    }

    #[test]
    fn connected_snapshot_recovers_a_missed_direct_path_event() {
        let network_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let route_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let mut peers = HashMap::new();
        let connection = connections::ConnectionData {
            connection_id: "conn_test".to_string(),
            room_id: "room_test".to_string(),
            host_player_id: "host".to_string(),
            peer_player_id: "member".to_string(),
            state: "CONNECTED".to_string(),
            selected_path: Some("LAN".to_string()),
            failure_reason: None,
            expires_at: "2026-08-28T01:00:00Z".to_string(),
            created_at: "2026-08-28T00:00:00Z".to_string(),
            updated_at: "2026-08-28T00:00:01Z".to_string(),
            candidates: vec![connections::ConnectionCandidate {
                candidate_id: "candidate-host-lan".to_string(),
                connection_id: "conn_test".to_string(),
                player_id: "host".to_string(),
                foundation: "toolbox-lan".to_string(),
                candidate_type: "LAN".to_string(),
                protocol: "UDP".to_string(),
                address: "192.168.1.10".to_string(),
                port: 42000,
                priority: 2_000_000,
                created_at: "2026-08-28T00:00:00Z".to_string(),
            }],
        };

        reconcile_connection_snapshot(
            LegacyRole::Member,
            "member",
            &network_socket,
            &route_status,
            &mut peers,
            &connection,
        );

        assert!(
            route_status
                .lock()
                .unwrap()
                .ready_connections
                .contains("conn_test")
        );
        let peer = peers.get("conn_test").unwrap();
        assert_eq!(peer.control_plane_state.as_deref(), Some("CONNECTED"));
        assert_eq!(peer.selected_direct_path.as_deref(), Some("LAN"));
        assert_eq!(peer.direct, Some("192.168.1.10:42000".parse().unwrap()));
    }

    #[test]
    fn member_candidate_reannouncement_is_bounded_and_stops_after_direct_checking() {
        let start = Instant::now();
        let mut peer = PeerPath {
            control_plane_state: Some("CHECKING_DIRECT".to_string()),
            ..PeerPath::default()
        };
        assert!(should_reannounce_member_candidates(&peer, start));

        note_candidate_announcement(&mut peer, start);
        assert!(!should_reannounce_member_candidates(
            &peer,
            start + Duration::from_millis(499)
        ));
        assert!(should_reannounce_member_candidates(
            &peer,
            start + Duration::from_millis(500)
        ));

        peer.control_plane_state = Some("ALLOCATING_RELAY".to_string());
        assert!(!should_reannounce_member_candidates(
            &peer,
            start + Duration::from_secs(30)
        ));
        peer.control_plane_state = Some("CONNECTED".to_string());
        peer.direct = Some("192.168.1.10:42000".parse().unwrap());
        assert!(!should_reannounce_member_candidates(
            &peer,
            start + Duration::from_secs(30)
        ));
        assert_eq!(candidate_reannouncement_delay(10), Duration::from_secs(4));
    }

    #[test]
    fn stale_candidate_reannouncement_rejection_is_not_a_carrier_failure() {
        assert!(is_stale_candidate_reannouncement_error(
            "INVALID_CONNECTION_STATE",
            "Connection is not gathering direct candidates."
        ));
        assert!(!is_stale_candidate_reannouncement_error(
            "INVALID_CONNECTION_STATE",
            "Connection is not checking a direct path."
        ));
        assert!(!is_stale_candidate_reannouncement_error(
            "CONNECTION_FORBIDDEN",
            "Connection is not gathering direct candidates."
        ));
    }

    #[test]
    fn recovered_member_candidate_drives_the_host_probe_to_a_success_report() {
        let host_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let member_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        host_socket.set_nonblocking(true).unwrap();
        member_socket.set_nonblocking(true).unwrap();
        let member_address = member_socket.local_addr().unwrap();
        let (outgoing, messages) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let mut host_peers = HashMap::from([(
            "conn_test".to_string(),
            PeerPath {
                candidates: HashMap::from([("LAN".to_string(), member_address)]),
                ..PeerPath::default()
            },
        )]);
        let game_socket = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let mut buffer = [0_u8; 128];

        advance_direct_probe(
            "conn_test",
            host_peers.get_mut("conn_test").unwrap(),
            &host_socket,
            &outgoing,
        );
        let deadline = Instant::now() + Duration::from_secs(1);
        while host_peers
            .get("conn_test")
            .and_then(|peer| peer.direct)
            .is_none()
            && Instant::now() < deadline
        {
            handle_network_datagrams(
                LegacyRole::Member,
                &member_socket,
                &game_socket,
                None,
                &outgoing,
                &mut HashMap::new(),
                &mut buffer,
                &Arc::new(Mutex::new(LegacyRouteStatus::default())),
            );
            handle_network_datagrams(
                LegacyRole::Host,
                &host_socket,
                &game_socket,
                None,
                &outgoing,
                &mut host_peers,
                &mut buffer,
                &Arc::new(Mutex::new(LegacyRouteStatus::default())),
            );
            thread::sleep(Duration::from_millis(2));
        }

        assert_eq!(
            host_peers.get("conn_test").and_then(|peer| peer.direct),
            Some(member_address)
        );
        let report = messages
            .try_iter()
            .find(|message| message["type"] == "connection.check_result")
            .unwrap();
        assert_eq!(report["payload"]["success"], true);
        assert_eq!(report["payload"]["path"], "LAN");
    }

    #[test]
    fn realtime_candidate_parser_constructs_ipv6_socket_addresses() {
        assert_eq!(
            parse_remote_candidate(
                Some("host"),
                "member",
                Some("IPV6"),
                Some("UDP"),
                Some("2001:db8::10"),
                Some(43000),
            ),
            Some(("IPV6".to_string(), "[2001:db8::10]:43000".parse().unwrap()))
        );
    }

    #[test]
    fn local_game_endpoint_is_learned_once_and_host_authority_cannot_be_replaced() {
        let first_member: SocketAddr = "127.0.0.1:50000".parse().unwrap();
        let rogue_member: SocketAddr = "127.0.0.1:50001".parse().unwrap();
        let mut member_peer = None;
        assert!(accept_game_datagram_source(
            LegacyRole::Member,
            &mut member_peer,
            first_member
        ));
        assert!(!accept_game_datagram_source(
            LegacyRole::Member,
            &mut member_peer,
            rogue_member
        ));
        assert_eq!(member_peer, Some(first_member));

        let authority: SocketAddr = "127.0.0.1:7777".parse().unwrap();
        let mut host_peer = Some(authority);
        assert!(accept_game_datagram_source(
            LegacyRole::Host,
            &mut host_peer,
            authority
        ));
        assert!(!accept_game_datagram_source(
            LegacyRole::Host,
            &mut host_peer,
            first_member
        ));
        assert_eq!(host_peer, Some(authority));
    }

    #[test]
    fn realtime_producer_rejects_oversized_messages_before_queueing() {
        let stop = Arc::new(AtomicBool::new(false));
        let (sender, receiver) = realtime_channel(stop.clone());
        assert!(
            sender
                .send(json!({"payload": "x".repeat(REALTIME_MAX_MESSAGE_BYTES)}))
                .is_err()
        );
        assert!(stop.load(Ordering::Acquire));
        assert!(receiver.try_recv().is_err());
    }

    #[test]
    fn realtime_producer_queue_overflow_stops_the_carrier_without_blocking() {
        let stop = Arc::new(AtomicBool::new(false));
        let (sender, receiver) = realtime_channel(stop.clone());
        for index in 0..REALTIME_QUEUE_CAPACITY {
            sender.send(json!({"index": index})).unwrap();
        }
        let started = Instant::now();
        assert!(matches!(
            sender.send(json!({"overflow": true})),
            Err(mpsc::TrySendError::Full(_))
        ));
        assert!(started.elapsed() < Duration::from_millis(100));
        assert!(stop.load(Ordering::Acquire));
        assert_eq!(receiver.try_iter().count(), REALTIME_QUEUE_CAPACITY);
    }

    #[test]
    fn websocket_loop_transmits_queued_candidate() {
        let listener = std::net::TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let address = listener.local_addr().unwrap();
        let (received_tx, received_rx) = mpsc::channel();
        let server = thread::spawn(move || {
            let (stream, _) = listener.accept().unwrap();
            let mut socket = tungstenite::accept(stream).unwrap();
            let message = socket.read().unwrap().into_text().unwrap();
            received_tx
                .send(serde_json::from_str::<Value>(&message).unwrap())
                .unwrap();
        });

        let (outgoing_tx, outgoing_rx) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let (events_tx, _events_rx) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let (ready_tx, ready_rx) = mpsc::channel();
        let stop = Arc::new(AtomicBool::new(false));
        let thread_stop = stop.clone();
        let client = thread::spawn(move || {
            websocket_loop_with_provider(
                format!("ws://{address}"),
                || Ok(SecretString::new("test-token")),
                outgoing_rx,
                events_tx,
                Some(ready_tx),
                thread_stop,
            )
        });
        ready_rx.recv_timeout(Duration::from_secs(3)).unwrap();
        outgoing_tx
            .send(candidate_event(
                "conn_test",
                "LAN",
                "192.168.10.20:40123".parse().unwrap(),
                2_000_000,
            ))
            .unwrap();
        let received = received_rx.recv_timeout(Duration::from_secs(3)).unwrap();
        assert_eq!(received["type"], "connection.candidate");
        assert_eq!(received["payload"]["connection_id"], "conn_test");

        stop.store(true, Ordering::Release);
        server.join().unwrap();
        client.join().unwrap();
    }

    #[test]
    fn websocket_reconnect_gets_rotated_credentials_and_requests_snapshot_resync() {
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let (headers_tx, headers_rx) = mpsc::channel();
        let server = thread::spawn(move || {
            for _ in 0..2 {
                let (stream, _) = listener.accept().unwrap();
                let headers_tx = headers_tx.clone();
                let mut socket = tungstenite::accept_hdr(
                    stream,
                    move |request: &tungstenite::handshake::server::Request, response| {
                        headers_tx
                            .send(
                                request.headers()["authorization"]
                                    .to_str()
                                    .unwrap()
                                    .to_string(),
                            )
                            .unwrap();
                        Ok(response)
                    },
                )
                .unwrap();
                socket.close(None).unwrap();
            }
        });
        let (_outgoing, outgoing_rx) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let (events_tx, events_rx) = realtime_channel(Arc::new(AtomicBool::new(false)));
        let stop = Arc::new(AtomicBool::new(false));
        let thread_stop = stop.clone();
        let client = thread::spawn(move || {
            let mut calls = 0;
            websocket_loop_with_provider(
                format!("ws://{address}"),
                move || {
                    calls += 1;
                    if calls > 2 {
                        anyhow::bail!("session changed");
                    }
                    Ok(SecretString::new(if calls == 1 {
                        "synthetic-old"
                    } else {
                        "synthetic-new"
                    }))
                },
                outgoing_rx,
                events_tx,
                None,
                thread_stop,
            );
        });
        assert_eq!(
            headers_rx.recv_timeout(Duration::from_secs(3)).unwrap(),
            "Bearer synthetic-old"
        );
        assert_eq!(
            headers_rx.recv_timeout(Duration::from_secs(3)).unwrap(),
            "Bearer synthetic-new"
        );
        let until = Instant::now() + Duration::from_secs(3);
        let mut resync = false;
        while Instant::now() < until {
            if let Ok(event) = events_rx.recv_timeout(Duration::from_millis(100))
                && event["type"] == "transport.resync_required"
            {
                resync = true;
                break;
            }
        }
        assert!(resync);
        stop.store(true, Ordering::Release);
        client.join().unwrap();
        server.join().unwrap();
    }

    #[test]
    fn bp014_toolbox_legacy_route_loop_counters() {
        let host_network = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let member_network = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let host_game = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let member_game = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let member_source = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let member_game_addr = member_game.local_addr().unwrap();
        let authority = UdpSocket::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        for socket in [
            &host_network,
            &member_network,
            &host_game,
            &member_game,
            &member_source,
            &authority,
        ] {
            socket.set_nonblocking(true).unwrap();
        }
        let host_addr = host_network.local_addr().unwrap();
        let member_addr = member_network.local_addr().unwrap();
        let candidate = |player_id: &str, address: SocketAddr| connections::ConnectionCandidate {
            candidate_id: format!("candidate-{player_id}"),
            connection_id: "bp014-route-connection".to_string(),
            player_id: player_id.to_string(),
            foundation: "bp014-route".to_string(),
            candidate_type: "LAN".to_string(),
            protocol: "UDP".to_string(),
            address: address.ip().to_string(),
            port: address.port(),
            priority: 2_000_000,
            created_at: "2026-09-08T00:00:00Z".to_string(),
        };
        let snapshot = |candidate: connections::ConnectionCandidate| connections::ConnectionData {
            connection_id: "bp014-route-connection".to_string(),
            room_id: "bp014-route-room".to_string(),
            host_player_id: "bp014-route-host".to_string(),
            peer_player_id: "bp014-route-member".to_string(),
            state: "CONNECTED".to_string(),
            selected_path: Some("LAN".to_string()),
            failure_reason: None,
            expires_at: "2026-09-09T00:00:00Z".to_string(),
            created_at: "2026-09-08T00:00:00Z".to_string(),
            updated_at: "2026-09-08T00:00:01Z".to_string(),
            candidates: vec![candidate],
        };
        let host_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let member_status = Arc::new(Mutex::new(LegacyRouteStatus::default()));
        let host_stop = Arc::new(AtomicBool::new(false));
        let member_stop = Arc::new(AtomicBool::new(false));
        let (host_outgoing, host_outgoing_messages) = realtime_channel(host_stop.clone());
        let (member_outgoing, member_outgoing_messages) = realtime_channel(member_stop.clone());
        let (_host_event_sender, host_events) = realtime_channel(host_stop.clone());
        let (_member_event_sender, member_events) = realtime_channel(member_stop.clone());
        let (_host_snapshot_sender, host_snapshots) = mpsc::sync_channel(REALTIME_QUEUE_CAPACITY);
        let (_member_snapshot_sender, member_snapshots) = mpsc::sync_channel(REALTIME_QUEUE_CAPACITY);
        let host_ids = Arc::new(Mutex::new(HashSet::from([
            "bp014-route-connection".to_string(),
        ])));
        let member_ids = Arc::new(Mutex::new(HashSet::from([
            "bp014-route-connection".to_string(),
        ])));
        let host_authority = Arc::new(Mutex::new(Some(authority.local_addr().unwrap())));
        let member_authority = Arc::new(Mutex::new(None));
        let host_thread = {
            let host_status = host_status.clone();
            let host_stop = host_stop.clone();
            let host_ids = host_ids.clone();
            let host_authority = host_authority.clone();
            thread::spawn(move || {
                route_loop(
                    LegacyRole::Host,
                    "bp014-route-room".to_string(),
                    "bp014-route-host".to_string(),
                    host_game,
                    host_network,
                    host_addr,
                    None,
                    Some("bp014-route-connection".to_string()),
                    Some(snapshot(candidate("bp014-route-member", member_addr))),
                    host_outgoing,
                    host_events,
                    host_snapshots,
                    host_ids,
                    host_status,
                    host_stop,
                    None,
                    0,
                    host_authority,
                )
            })
        };
        let member_thread = {
            let member_status = member_status.clone();
            let member_stop = member_stop.clone();
            let member_ids = member_ids.clone();
            let member_authority = member_authority.clone();
            thread::spawn(move || {
                route_loop(
                    LegacyRole::Member,
                    "bp014-route-room".to_string(),
                    "bp014-route-member".to_string(),
                    member_game,
                    member_network,
                    member_addr,
                    None,
                    Some("bp014-route-connection".to_string()),
                    Some(snapshot(candidate("bp014-route-host", host_addr))),
                    member_outgoing,
                    member_events,
                    member_snapshots,
                    member_ids,
                    member_status,
                    member_stop,
                    None,
                    0,
                    member_authority,
                )
            })
        };
        let mut buffer = [0_u8; 1500];
        member_source
            .send_to(b"member-to-host", member_game_addr)
            .unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut authority_source = None;
        while Instant::now() < deadline {
            match authority.recv_from(&mut buffer) {
                Ok((count, source)) => {
                    assert_eq!(&buffer[..count], b"member-to-host");
                    authority_source = Some(source);
                    break;
                }
                Err(error) if error.kind() == ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(2));
                }
                Err(error) => panic!("authority receive failed: {error}"),
            }
        }
        let authority_source = authority_source.expect("host route did not deliver member datagram");
        authority.send_to(b"host-to-member", authority_source).unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut received = None;
        while Instant::now() < deadline {
            match member_source.recv_from(&mut buffer) {
                Ok((count, _)) => {
                    received = Some(count);
                    break;
                }
                Err(error) if error.kind() == ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(2));
                }
                Err(error) => panic!("member source receive failed: {error}"),
            }
        }
        assert_eq!(&buffer[..received.expect("member route did not receive reply")], b"host-to-member");
        host_stop.store(true, Ordering::Release);
        member_stop.store(true, Ordering::Release);
        host_thread.join().unwrap();
        member_thread.join().unwrap();
        drop(host_outgoing_messages);
        drop(member_outgoing_messages);
        let host_snapshot = host_status.lock().unwrap();
        let member_snapshot = member_status.lock().unwrap();
        println!(
            "BP014_TOOLBOX_LEGACY_ROUTE_STATS source=LegacyRouteStatus host_datagrams_sent={} host_datagrams_received={} member_datagrams_sent={} member_datagrams_received={}",
            host_snapshot.datagrams_sent,
            host_snapshot.datagrams_received,
            member_snapshot.datagrams_sent,
            member_snapshot.datagrams_received,
        );
        assert_eq!(host_snapshot.datagrams_sent, 1);
        assert_eq!(host_snapshot.datagrams_received, 1);
        assert_eq!(member_snapshot.datagrams_sent, 1);
        assert_eq!(member_snapshot.datagrams_received, 1);
    }

}
