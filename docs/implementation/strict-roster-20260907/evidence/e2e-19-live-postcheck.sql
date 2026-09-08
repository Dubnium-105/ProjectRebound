SELECT 'schema_max=' || COALESCE(MAX(version),0) FROM schema_migrations;
SELECT 'schema48_rows=' || COUNT(*) FROM schema_migrations WHERE version=48;
SELECT 'e2e19_lobbies_after_driver=' || COUNT(*) FROM match_lobbies WHERE id LIKE 'e2e19-live-%';
SELECT 'e2e19_admin_users_after_driver=' || COUNT(*) FROM admin_users WHERE id LIKE 'e2e19-live-%';
SELECT 'e2e19_admin_sessions_after_driver=' || COUNT(*) FROM admin_sessions WHERE id LIKE 'e2e19-live-%';
SELECT 'e2e19_active_attempts_after_driver=' || COUNT(*) FROM match_attempts WHERE id LIKE 'e2e19-live-%';
