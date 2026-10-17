-- Local durable parking queue. Original Kafka bytes and coordinates are immutable.
CREATE TABLE consumer_quarantine (
 id BIGSERIAL PRIMARY KEY,
 topic TEXT NOT NULL,
 partition INTEGER NOT NULL,
 offset_value BIGINT NOT NULL,
 message_key BYTEA NOT NULL,
 body BYTEA NOT NULL,
 reason TEXT NOT NULL,
 attempts INTEGER NOT NULL CHECK (attempts >= 0),
 event_id TEXT NOT NULL DEFAULT '',
 correlation_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 resolved_at TIMESTAMPTZ,
 UNIQUE(topic,partition,offset_value)
);
CREATE INDEX consumer_quarantine_open ON consumer_quarantine(id) WHERE resolved_at IS NULL;
CREATE TABLE consumer_replay_audit (
 id BIGSERIAL PRIMARY KEY,
 failure_id BIGINT NOT NULL REFERENCES consumer_quarantine(id),
 operator_name TEXT NOT NULL,
 note TEXT NOT NULL,
 recovery_body BYTEA NOT NULL,
 recovery_key BYTEA NOT NULL,
 succeeded BOOLEAN NOT NULL DEFAULT false,
 attempted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
