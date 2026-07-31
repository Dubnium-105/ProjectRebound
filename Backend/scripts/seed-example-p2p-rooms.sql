BEGIN;

-- Persistent P2P directory fixtures. They are deliberately CLOSED and use
-- non-Steam fixture identifiers and DELETED fixture players, so clients can
-- render/filter the records but cannot mistake them for joinable production rooms.
INSERT INTO players (
    id,
    steam_id,
    persona_name,
    account_status,
    is_vip,
    auth_provider,
    auth_level,
    last_login_at,
    created_at,
    updated_at
) VALUES
    ('p_example_p2p_hk_host_01', '90000000000000101', '[示例] 香港房主', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_sg_host_01', '90000000000000102', '[示例] 新加坡房主', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_sg_peer_01', '90000000000000103', '[示例] 新加坡队员', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_jp_host_01', '90000000000000104', '[示例] 东京房主', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_jp_peer_01', '90000000000000105', '[示例] 东京队员 A', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_jp_peer_02', '90000000000000106', '[示例] 东京队员 B', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_jp_peer_03', '90000000000000107', '[示例] 东京队员 C', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_us_host_01', '90000000000000108', '[示例] 美西房主', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_us_peer_01', '90000000000000109', '[示例] 美西队员 A', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW()),
    ('p_example_p2p_us_peer_02', '90000000000000110', '[示例] 美西队员 B', 'DELETED', FALSE, 'steam_client_asserted', 'unverified', NULL, NOW(), NOW())
ON CONFLICT (id) DO UPDATE SET
    steam_id = EXCLUDED.steam_id,
    persona_name = EXCLUDED.persona_name,
    account_status = EXCLUDED.account_status,
    is_vip = EXCLUDED.is_vip,
    auth_provider = EXCLUDED.auth_provider,
    auth_level = EXCLUDED.auth_level,
    last_login_at = EXCLUDED.last_login_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO p2p_rooms (
    id,
    host_player_id,
    host_token_hash,
    display_name,
    region,
    mode,
    version,
    max_players,
    player_count,
    state,
    last_heartbeat_at,
    created_at,
    updated_at,
    closed_at
) VALUES
    (
        'room_example_hk_casual_01',
        'p_example_p2p_hk_host_01',
        decode(repeat('a1', 32), 'hex'),
        '[示例] 香港休闲 P2P 对局',
        'asia-hk',
        'casual',
        '0.0.0-fixture',
        4,
        1,
        'CLOSED',
        NOW(),
        NOW(),
        NOW(),
        NOW()
    ),
    (
        'room_example_sg_ranked_01',
        'p_example_p2p_sg_host_01',
        decode(repeat('a2', 32), 'hex'),
        '[示例] 新加坡竞技 P2P 对局',
        'asia-sg',
        'ranked',
        '0.0.0-fixture',
        8,
        2,
        'CLOSED',
        NOW(),
        NOW(),
        NOW(),
        NOW()
    ),
    (
        'room_example_jp_coop_01',
        'p_example_p2p_jp_host_01',
        decode(repeat('a3', 32), 'hex'),
        '[示例] 东京合作 P2P 对局（满员）',
        'asia-jp',
        'coop',
        '0.0.0-fixture',
        4,
        4,
        'CLOSED',
        NOW(),
        NOW(),
        NOW(),
        NOW()
    ),
    (
        'room_example_us_survival_01',
        'p_example_p2p_us_host_01',
        decode(repeat('a4', 32), 'hex'),
        '[示例] 美西生存 P2P 对局',
        'us-west',
        'survival',
        '0.0.0-fixture',
        8,
        3,
        'CLOSED',
        NOW(),
        NOW(),
        NOW(),
        NOW()
    )
ON CONFLICT (id) DO UPDATE SET
    host_player_id = EXCLUDED.host_player_id,
    host_token_hash = EXCLUDED.host_token_hash,
    display_name = EXCLUDED.display_name,
    region = EXCLUDED.region,
    mode = EXCLUDED.mode,
    version = EXCLUDED.version,
    max_players = EXCLUDED.max_players,
    player_count = EXCLUDED.player_count,
    state = EXCLUDED.state,
    last_heartbeat_at = EXCLUDED.last_heartbeat_at,
    updated_at = EXCLUDED.updated_at,
    closed_at = EXCLUDED.closed_at;

INSERT INTO p2p_room_members (
    room_id,
    player_id,
    role,
    status,
    joined_at,
    left_at
) VALUES
    ('room_example_hk_casual_01', 'p_example_p2p_hk_host_01', 'HOST', 'LEFT', NOW(), NOW()),
    ('room_example_sg_ranked_01', 'p_example_p2p_sg_host_01', 'HOST', 'LEFT', NOW(), NOW()),
    ('room_example_sg_ranked_01', 'p_example_p2p_sg_peer_01', 'MEMBER', 'LEFT', NOW(), NOW()),
    ('room_example_jp_coop_01', 'p_example_p2p_jp_host_01', 'HOST', 'LEFT', NOW(), NOW()),
    ('room_example_jp_coop_01', 'p_example_p2p_jp_peer_01', 'MEMBER', 'LEFT', NOW(), NOW()),
    ('room_example_jp_coop_01', 'p_example_p2p_jp_peer_02', 'MEMBER', 'LEFT', NOW(), NOW()),
    ('room_example_jp_coop_01', 'p_example_p2p_jp_peer_03', 'MEMBER', 'LEFT', NOW(), NOW()),
    ('room_example_us_survival_01', 'p_example_p2p_us_host_01', 'HOST', 'LEFT', NOW(), NOW()),
    ('room_example_us_survival_01', 'p_example_p2p_us_peer_01', 'MEMBER', 'LEFT', NOW(), NOW()),
    ('room_example_us_survival_01', 'p_example_p2p_us_peer_02', 'MEMBER', 'LEFT', NOW(), NOW())
ON CONFLICT (room_id, player_id) DO UPDATE SET
    role = EXCLUDED.role,
    status = EXCLUDED.status,
    joined_at = EXCLUDED.joined_at,
    left_at = EXCLUDED.left_at;

COMMIT;
