DROP TABLE IF EXISTS silences;
DROP TABLE IF EXISTS alert_events;
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS alert_rules;
DROP TABLE IF EXISTS notification_channels;
DROP FUNCTION IF EXISTS opshub_maintain_monitor_partitions(integer);
DROP TABLE IF EXISTS monitor_results_hourly;
DROP TABLE IF EXISTS monitor_results; -- drops its partitions
DROP TABLE IF EXISTS monitors;
DROP TYPE IF EXISTS channel_kind;
DROP TYPE IF EXISTS alert_status;
DROP TYPE IF EXISTS alert_severity;
DROP TYPE IF EXISTS alert_rule_kind;
DROP TYPE IF EXISTS monitor_kind;
