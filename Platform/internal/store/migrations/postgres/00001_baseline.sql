-- +goose Up
-- Existing installations are adopted through idempotent table and index creation.
CREATE TABLE IF NOT EXISTS documents(kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(kind,id));

CREATE TABLE IF NOT EXISTS document_versions(kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(kind,id,version));

CREATE INDEX IF NOT EXISTS document_time ON document_versions(kind,id,updated_ms);

CREATE TABLE IF NOT EXISTS sync_changes(sequence BIGINT PRIMARY KEY,kind TEXT NOT NULL,id TEXT NOT NULL,version BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS inbox(id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,source_id TEXT NOT NULL,received_ms BIGINT NOT NULL,expires_ms BIGINT NOT NULL DEFAULT 0);

CREATE TABLE IF NOT EXISTS data_gaps(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,key TEXT NOT NULL,from_ms BIGINT NOT NULL,to_ms BIGINT NOT NULL,data TEXT NOT NULL);

CREATE INDEX IF NOT EXISTS data_gaps_scope ON data_gaps(device_id,key,to_ms,from_ms);

CREATE TABLE IF NOT EXISTS outbox(id TEXT PRIMARY KEY,kind TEXT NOT NULL,destination TEXT NOT NULL,payload TEXT NOT NULL,created_ms BIGINT NOT NULL,attempts INTEGER NOT NULL,next_ms BIGINT NOT NULL,last_error TEXT NOT NULL);

CREATE INDEX IF NOT EXISTS outbox_due ON outbox(kind,next_ms,created_ms,id);

CREATE TABLE IF NOT EXISTS audit(source_id TEXT NOT NULL,sequence BIGINT NOT NULL,occurred_ms BIGINT NOT NULL,received_ms BIGINT NOT NULL,request_id TEXT NOT NULL,previous_hash TEXT NOT NULL,hash TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(source_id,sequence));

CREATE TABLE IF NOT EXISTS audit_checkpoints(source_id TEXT NOT NULL,sequence BIGINT NOT NULL,hash TEXT NOT NULL,signature TEXT NOT NULL,public_key TEXT NOT NULL,PRIMARY KEY(source_id,sequence));

CREATE TABLE IF NOT EXISTS latest(device_id TEXT NOT NULL,key TEXT NOT NULL,observed_ms BIGINT NOT NULL,received_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(device_id,key));

CREATE TABLE IF NOT EXISTS observations(id TEXT NOT NULL,observed_ms BIGINT NOT NULL,message_id TEXT NOT NULL,device_id TEXT NOT NULL,key TEXT NOT NULL,received_ms BIGINT NOT NULL,revision BIGINT NOT NULL,quality TEXT NOT NULL,definition_id TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(id,observed_ms)) PARTITION BY RANGE (observed_ms);

CREATE INDEX IF NOT EXISTS observations_query ON observations(device_id,key,observed_ms,received_ms);

CREATE INDEX IF NOT EXISTS observations_recent ON observations(observed_ms DESC,device_id,key);

CREATE INDEX IF NOT EXISTS observations_revisions ON observations(device_id,key,observed_ms,revision DESC,received_ms DESC,id DESC) WHERE definition_id<>'';

CREATE TABLE IF NOT EXISTS observation_archives(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,key TEXT NOT NULL,first_ms BIGINT NOT NULL,last_ms BIGINT NOT NULL,raw_first_ms BIGINT NOT NULL,raw_last_ms BIGINT NOT NULL,point_count BIGINT NOT NULL,plain_bytes BIGINT NOT NULL,sha256 TEXT NOT NULL,payload BYTEA NOT NULL);

CREATE INDEX IF NOT EXISTS observation_archives_scope ON observation_archives(device_id,key,last_ms,first_ms);

CREATE INDEX IF NOT EXISTS observation_archives_time ON observation_archives(last_ms,first_ms);

CREATE TABLE IF NOT EXISTS rollups(device_id TEXT NOT NULL,key TEXT NOT NULL,granularity TEXT NOT NULL,bucket_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(device_id,key,granularity,bucket_ms));

CREATE TABLE IF NOT EXISTS action_journal(command_id TEXT PRIMARY KEY,payload_hash TEXT NOT NULL,status TEXT NOT NULL,fence BIGINT NOT NULL,data TEXT NOT NULL,updated_ms BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS engine_checkpoints(state_id TEXT NOT NULL,bucket_ms BIGINT NOT NULL,at_ms BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(state_id,bucket_ms));
