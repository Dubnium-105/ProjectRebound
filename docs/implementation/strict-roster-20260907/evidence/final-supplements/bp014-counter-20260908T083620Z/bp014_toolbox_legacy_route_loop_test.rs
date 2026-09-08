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
