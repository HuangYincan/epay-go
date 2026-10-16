CREATE TABLE observer_requests (
    key_id varchar(64) NOT NULL REFERENCES observer_credentials(key_id),
    request_id text NOT NULL CHECK (request_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    envelope_sha256 text NOT NULL CHECK (envelope_sha256 ~ '^[0-9a-f]{64}$'),
    event_pk bigint NOT NULL REFERENCES observer_events(id),
    disposition text NOT NULL CHECK (disposition IN ('stored','duplicate','conflict')),
    first_seen_at_ms bigint NOT NULL CHECK (first_seen_at_ms BETWEEN 0 AND 9007199254740991),
    PRIMARY KEY (key_id,request_id)
);
CREATE TRIGGER observer_request_guard BEFORE UPDATE OR DELETE ON observer_requests FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TABLE observer_device_budgets (
    environment varchar(64) NOT NULL,
    device_id varchar(64) NOT NULL,
    tokens_milli integer NOT NULL CHECK (tokens_milli BETWEEN 0 AND 20000),
    last_refill_ms bigint NOT NULL CHECK (last_refill_ms BETWEEN 0 AND 9007199254740991),
    PRIMARY KEY (environment,device_id),
    FOREIGN KEY (environment,device_id) REFERENCES observer_devices(environment,device_id)
);
