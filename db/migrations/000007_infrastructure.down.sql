DROP TABLE IF EXISTS ssl_certificates;
DROP FUNCTION IF EXISTS opshub_maintain_metric_partitions(integer);
DROP TABLE IF EXISTS asset_metrics_hourly;
DROP TABLE IF EXISTS asset_metrics; -- drops its partitions
DROP TABLE IF EXISTS infra_assets;
DROP TYPE IF EXISTS asset_kind;
