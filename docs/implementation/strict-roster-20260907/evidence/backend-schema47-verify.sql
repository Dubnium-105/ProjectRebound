\set ON_ERROR_STOP on
select current_database() as database, current_user as role;
select max(version) as max_migration, count(*) as migration_rows from schema_migrations;
select version, name, checksum from schema_migrations where version in (45,46,47) order by version;
select 'match_attempts' as table_name, count(*) as rows from match_attempts;
select 'match_attempt_roster' as table_name, count(*) as rows from match_attempt_roster;
select 'match_admission_grants' as table_name, count(*) as rows from match_admission_grants;
select 'p2p_room_members' as table_name, count(*) as rows from p2p_room_members;
select 'p2p_match_roster' as table_name, count(*) as rows from p2p_match_roster;
select 'p2p_match_sessions' as table_name, count(*) as rows from p2p_match_sessions;
select 'p2p_vnt_member_sessions' as table_name, count(*) as rows from p2p_vnt_member_sessions;
select 'p2p_vnt_sessions' as table_name, count(*) as rows from p2p_vnt_sessions;
select 'connections' as table_name, count(*) as rows from connections;
select 'connection_candidates' as table_name, count(*) as rows from connection_candidates;
select 'game_servers' as table_name, count(*) as rows from game_servers;
select count(*) as public_columns from information_schema.columns where table_schema='public';
select count(*) as public_constraints from pg_constraint where connamespace='public'::regnamespace;
select count(*) as public_indexes from pg_indexes where schemaname='public';


