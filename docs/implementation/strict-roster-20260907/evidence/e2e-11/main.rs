use anyhow::{Context, Result, ensure};
use rebound_toolbox::{
    api::infrastructure::http,
    config,
    launching::metatunnel::MetaTunnelSession,
    security::auth,
    vnt::legacy::RealtimeFeed,
};
use serde_json::Value;
use std::{
    fs,
    io::{Read, Write},
    net::{SocketAddr, TcpStream},
    path::PathBuf,
    process::{Command, Stdio},
    sync::{Arc, Barrier},
    thread,
    time::{Duration, Instant},
};

const OLD: &str = "synthetic-old-access-000000000000000000000000";
const NEW: &str = "synthetic-new-access-111111111111111111111111";
const REFRESH_OLD: &str = "synthetic-old-refresh-000000000000000000000";
const REFRESH_NEW: &str = "synthetic-new-refresh-111111111111111111111";
const LOGOUT_OLD: &str = "synthetic-logout-old-222222222222222222222";
const LOGOUT_REFRESH: &str = "synthetic-logout-refresh-222222222222222";
const SWITCH_OLD: &str = "synthetic-switch-old-333333333333333333333";
const SWITCH_REFRESH: &str = "synthetic-switch-refresh-333333333333333";
const SWITCH_NEW: &str = "synthetic-switch-new-444444444444444444444";
const SWITCH_NEW_REFRESH: &str = "synthetic-switch-new-refresh-44444444444";

fn marker(root: &PathBuf, name: &str) -> PathBuf {
    root.join(name)
}

fn write_marker(root: &PathBuf, name: &str) -> Result<()> {
    fs::write(marker(root, name), b"ok")?;
    Ok(())
}

fn wait_marker(root: &PathBuf, name: &str, timeout: Duration) -> Result<()> {
    let deadline = Instant::now() + timeout;
    while !marker(root, name).exists() {
        ensure!(Instant::now() < deadline, "timed out waiting for marker {name}");
        thread::sleep(Duration::from_millis(20));
    }
    Ok(())
}

fn wait_marker_optional(root: &PathBuf, name: &str, timeout: Duration) -> bool {
    wait_marker(root, name, timeout).is_ok()
}

fn set_phase(root: &PathBuf, phase: &str) -> Result<()> {
    fs::write(root.join("phase"), phase.as_bytes())?;
    Ok(())
}

fn set_session(epoch: u64, access: &str, refresh: &str, steam_id: &str) -> Result<()> {
    config::update_config(|cfg| {
        cfg.session_epoch = epoch;
        cfg.access_token = access.to_string();
        cfg.refresh_token = refresh.to_string();
        cfg.access_token_expires_at = "2099-01-01T00:00:00Z".into();
        cfg.refresh_token_expires_at = "2099-01-01T00:00:00Z".into();
        cfg.steam_id = steam_id.to_string();
        Ok(())
    })?;
    Ok(())
}

fn run_pending_request(token: &'static str) -> thread::JoinHandle<Result<()>> {
    thread::spawn(move || {
        let _: Value = http::get_auth("/test/protected", token)?;
        Ok(())
    })
}

fn interrupt_config_writer(expected_epoch: u64, expected_access: &str) -> Result<()> {
    let writer = std::env::var("E2E11_CONFIG_WRITER")
        .context("E2E11_CONFIG_WRITER is required for the integrated config interruption")?;
    let path = config::app_data_root()?.join("app_config.json");
    let ready = path.with_extension("ready");
    if ready.exists() {
        fs::remove_file(&ready)?;
    }

    let mut child = Command::new(writer);
    child
        .args([
            "--exact",
            "config::config_types::tests::killed_config_writer_recovers_complete_previous_version",
            "--nocapture",
        ])
        .env("REBOUND_TEST_PAUSE_CONFIG_REPLACE", &path)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let mut child = child.spawn().context("spawn config interruption writer")?;
    let deadline = Instant::now() + Duration::from_secs(20);
    while !ready.exists() && Instant::now() < deadline {
        if let Some(status) = child.try_wait()? {
            anyhow::bail!("config interruption writer exited before ciphertext flush: {status}");
        }
        thread::sleep(Duration::from_millis(20));
    }
    ensure!(ready.exists(), "config interruption writer did not reach ciphertext flush");
    let _ = child.kill();
    let status = child.wait()?;
    ensure!(
        !status.success(),
        "config interruption writer unexpectedly completed its replacement"
    );

    let recovered = config::load_config();
    ensure!(
        recovered.session_epoch == expected_epoch && recovered.access_token == expected_access,
        "config recovery changed the live session after an interrupted ciphertext replacement"
    );
    ensure!(path.is_file() && fs::metadata(&path)?.len() > 0);
    let parent = path.parent().context("config path has no parent")?;
    ensure!(
        !fs::read_dir(parent)?.any(|entry| {
            entry
                .ok()
                .map(|entry| {
                    entry
                        .file_name()
                        .to_string_lossy()
                        .starts_with(".app_config.pending-")
                })
                .unwrap_or(false)
        }),
        "interrupted config ciphertext was not cleaned up"
    );
    Ok(())
}

fn main() -> Result<()> {
    let root = PathBuf::from(std::env::var("E2E11_STATE_DIR")?)
        .canonicalize()
        .context("canonicalize E2E-11 state directory")?;
    let realtime_url = std::env::var("E2E11_REALTIME_URL")?;
    ensure!(realtime_url.starts_with("ws://127.0.0.1:"));
    ensure!(root.join("phase").exists(), "harness phase marker is missing");

    set_phase(&root, "phase_a")?;
    set_session(7, OLD, REFRESH_OLD, "synthetic-player-a")?;

    let ws_root = root.clone();
    let ws_url = realtime_url.clone();
    let feed_thread = thread::spawn(move || -> Result<bool> {
        let feed = RealtimeFeed::connect(&ws_url, OLD)?;
        let deadline = Instant::now() + Duration::from_secs(20);
        let mut resync = false;
        while Instant::now() < deadline {
            if let Some(event) = feed.try_recv()
                && event["type"] == "transport.resync_required"
            {
                resync = true;
            }
            if resync && marker(&ws_root, "ws-new").exists() {
                break;
            }
            thread::sleep(Duration::from_millis(20));
        }
        ensure!(resync, "realtime feed did not emit resync_required");
        ensure!(marker(&ws_root, "ws-new").exists(), "realtime did not reconnect with new token");
        feed.ensure_running()?;
        drop(feed);
        Ok(true)
    });
    ensure!(
        wait_marker_optional(&root, "ws-old", Duration::from_secs(10)),
        "Realtime did not reach its old-token handshake"
    );

    // Start MetaTunnel while the old epoch is still current. Its real profile
    // preflight receives one 401 and owns the single refresh lock; the HTTP
    // fan-out below then joins that same in-flight rotation.
    let meta_thread = thread::spawn(MetaTunnelSession::start);
    ensure!(
        wait_marker_optional(&root, "meta-old", Duration::from_secs(30)),
        "MetaTunnel did not reach the old-token profile request"
    );
    wait_marker(&root, "refresh-started-phase_a", Duration::from_secs(10))?;

    let barrier = Arc::new(Barrier::new(20));
    let workers = (0..20)
        .map(|_| {
            let barrier = barrier.clone();
            thread::spawn(move || -> Result<()> {
                barrier.wait();
                let _: Value = http::get_auth("/test/protected", OLD)?;
                Ok(())
            })
        })
        .collect::<Vec<_>>();
    wait_marker(&root, "old401-20", Duration::from_secs(20))?;
    write_marker(&root, "old401-release")?;
    write_marker(&root, "release-refresh-phase_a")?;

    let mut http_errors = Vec::new();
    for worker in workers {
        match worker.join().map_err(|_| anyhow::anyhow!("HTTP worker panicked"))? {
            Ok(()) => {}
            Err(error) => http_errors.push(format!("{error:#}")),
        }
    }
    if !http_errors.is_empty() {
        let cfg = config::load_config();
        eprintln!(
            "HTTP phase A errors={} final_epoch={} access_is_new={}",
            http_errors.len(),
            cfg.session_epoch,
            cfg.access_token == NEW
        );
        return Err(anyhow::anyhow!("HTTP workers failed: {}", http_errors.join(" | ")));
    }
    let meta = meta_thread
        .join()
        .map_err(|_| anyhow::anyhow!("MetaTunnel worker panicked"))??;
    let feed_resync = feed_thread
        .join()
        .map_err(|_| anyhow::anyhow!("Realtime worker panicked"))??;
    ensure!(config::load_config().access_token == NEW, "phase A token was not committed");
    ensure!(marker(&root, "meta-new").exists(), "MetaTunnel profile did not retry with new token");

    let endpoint: SocketAddr = meta.endpoints().logic_endpoint.parse()?;
    let mut logic = TcpStream::connect_timeout(&endpoint, Duration::from_secs(5))?;
    logic.set_read_timeout(Some(Duration::from_secs(5)))?;
    logic.write_all(b"E2E11")?;
    let mut echoed = [0_u8; 5];
    logic.read_exact(&mut echoed)?;
    ensure!(&echoed == b"E2E11", "MetaTunnel logic bridge did not echo the probe");
    drop(logic);
    drop(meta);

    interrupt_config_writer(7, NEW)?;
    write_marker(&root, "config-interruption-recovered")?;

    set_phase(&root, "logout")?;
    set_session(20, LOGOUT_OLD, LOGOUT_REFRESH, "synthetic-player-logout")?;
    let logout_request = run_pending_request(LOGOUT_OLD);
    wait_marker(&root, "refresh-started-logout", Duration::from_secs(10))?;
    auth::logout()?;
    write_marker(&root, "release-refresh-logout")?;
    ensure!(
        logout_request
            .join()
            .map_err(|_| anyhow::anyhow!("logout refresh worker panicked"))?
            .is_err(),
        "late refresh crossed logout epoch"
    );
    let logged_out = config::load_config();
    ensure!(logged_out.session_epoch == 21);
    ensure!(logged_out.access_token.is_empty() && logged_out.refresh_token.is_empty());

    set_phase(&root, "switch")?;
    set_session(30, SWITCH_OLD, SWITCH_REFRESH, "synthetic-player-old")?;
    let switch_request = run_pending_request(SWITCH_OLD);
    wait_marker(&root, "refresh-started-switch", Duration::from_secs(10))?;
    set_session(31, SWITCH_NEW, SWITCH_NEW_REFRESH, "synthetic-player-new")?;
    write_marker(&root, "release-refresh-switch")?;
    ensure!(
        switch_request
            .join()
            .map_err(|_| anyhow::anyhow!("switch refresh worker panicked"))?
            .is_err(),
        "late refresh crossed account-switch epoch"
    );
    let switched = config::load_config();
    ensure!(switched.session_epoch == 31);
    ensure!(switched.access_token == SWITCH_NEW && switched.refresh_token == SWITCH_NEW_REFRESH);
    ensure!(switched.steam_id == "synthetic-player-new");

    println!(
        "PASS: integrated HTTP 401/refresh + MetaTunnel token handoff + WS reconnect + logout/switch epoch barriers (resync={feed_resync}, logic_bridge=PASS)"
    );
    Ok(())
}
