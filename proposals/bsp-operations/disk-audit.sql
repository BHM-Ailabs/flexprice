-- Operator-run, read-only PostgreSQL 18 audit. Run inside each database whose
-- size matters (FlexPrice plus Temporal/visibility if on this cluster).
-- Never print a connection string, archive_command, credentials or data rows.
BEGIN READ ONLY;
SET LOCAL statement_timeout = '3s';
SET LOCAL lock_timeout = '1s';

SELECT now() AS sampled_at, pg_database_size(current_database()) AS database_bytes;
SELECT datname, pg_database_size(oid) AS database_bytes
FROM pg_database WHERE datallowconn ORDER BY database_bytes DESC;

SELECT schemaname, relname, pg_table_size(relid) AS table_toast_bytes,
       pg_indexes_size(relid) AS index_bytes,
       pg_total_relation_size(relid) AS total_bytes,
       n_live_tup, n_dead_tup, last_autovacuum, last_autoanalyze
FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 30;

SELECT sum(pg_table_size(relid)) AS table_toast_bytes,
       sum(pg_indexes_size(relid)) AS index_bytes,
       sum(pg_total_relation_size(relid)) AS relation_bytes
FROM pg_stat_user_tables;

SELECT count(*) AS wal_segments, sum(size) AS wal_bytes,
       min(modification) AS oldest_file_mtime, max(modification) AS newest_file_mtime
FROM pg_ls_waldir();

-- Values restricted to non-secret numeric/enum settings. Never SELECT * here.
SELECT name, setting, unit FROM pg_settings WHERE name IN (
 'archive_mode', 'archive_timeout', 'max_wal_size', 'min_wal_size',
 'wal_keep_size', 'max_slot_wal_keep_size', 'checkpoint_timeout',
 'checkpoint_completion_target', 'wal_compression', 'autovacuum',
 'autovacuum_vacuum_scale_factor', 'autovacuum_analyze_scale_factor',
 'autovacuum_vacuum_threshold', 'autovacuum_analyze_threshold',
 'autovacuum_max_workers', 'autovacuum_naptime', 'log_temp_files'
) ORDER BY name;
SELECT CASE WHEN current_setting('archive_command') LIKE '%pgbackrest%'
       THEN 'pgbackrest wrapper' WHEN current_setting('archive_command') = ''
       THEN 'empty' ELSE 'other (redacted)' END AS archive_implementation;
SELECT archived_count, last_archived_wal, last_archived_time,
       failed_count, last_failed_wal, last_failed_time, stats_reset
FROM pg_stat_archiver;
SELECT slot_type, active, wal_status, safe_wal_size,
       pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) AS retained_wal_bytes
FROM pg_replication_slots;
SELECT datname, temp_bytes, deadlocks, stats_reset FROM pg_stat_database;
COMMIT;
