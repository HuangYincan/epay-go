CREATE TABLE observer_environments (
    environment varchar(64) PRIMARY KEY CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    source_class text NOT NULL CHECK (source_class IN ('synthetic', 'notification')),
    enabled boolean NOT NULL DEFAULT false,
    UNIQUE (environment, source_class)
);
CREATE TABLE observer_receivers (
    environment varchar(64) NOT NULL REFERENCES observer_environments(environment),
    receiver_id varchar(64) NOT NULL CHECK (receiver_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    provider text NOT NULL CHECK (provider IN ('wxpay', 'alipay')),
    account_ref varchar(64) NOT NULL CHECK (account_ref ~ '^[A-Za-z0-9_-]{1,64}$'),
    enabled boolean NOT NULL DEFAULT false,
    PRIMARY KEY (environment, receiver_id),
    UNIQUE (environment, provider, account_ref),
    UNIQUE (environment, receiver_id, provider)
);
CREATE TABLE observer_channel_aliases (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE RESTRICT,
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    provider text NOT NULL,
    PRIMARY KEY (channel_id, provider),
    FOREIGN KEY (environment, receiver_id, provider) REFERENCES observer_receivers(environment, receiver_id, provider) ON DELETE RESTRICT
);
CREATE TABLE observer_devices (
    environment varchar(64) NOT NULL REFERENCES observer_environments(environment),
    device_id varchar(64) NOT NULL CHECK (device_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    enabled boolean NOT NULL DEFAULT false,
    PRIMARY KEY (environment, device_id)
);
CREATE TABLE observer_bindings (
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    binding_version integer NOT NULL CHECK (binding_version > 0),
    device_id varchar(64) NOT NULL,
    provider text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    PRIMARY KEY (environment, receiver_id, binding_version),
    FOREIGN KEY (environment, receiver_id, provider) REFERENCES observer_receivers(environment, receiver_id, provider) ON DELETE RESTRICT,
    FOREIGN KEY (environment, device_id) REFERENCES observer_devices(environment, device_id) ON DELETE RESTRICT,
    UNIQUE (environment, receiver_id, binding_version, device_id, provider)
);
CREATE UNIQUE INDEX observer_one_active_receiver ON observer_bindings(environment, receiver_id) WHERE active;
CREATE UNIQUE INDEX observer_one_active_device_provider ON observer_bindings(environment, device_id, provider) WHERE active;
CREATE TABLE observer_credentials (
    key_id varchar(64) PRIMARY KEY CHECK (key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    binding_version integer NOT NULL,
    device_id varchar(64) NOT NULL,
    provider text NOT NULL,
    source_class text NOT NULL,
    encryption_key_id varchar(64) NOT NULL CHECK (encryption_key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    key_ciphertext bytea NOT NULL CHECK (octet_length(key_ciphertext) = 60),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'revoked')),
    not_before_ms bigint NOT NULL CHECK (not_before_ms BETWEEN 0 AND 9007199254740991),
    expires_at_ms bigint NOT NULL CHECK (expires_at_ms BETWEEN 0 AND 9007199254740991 AND expires_at_ms > not_before_ms),
    revoked_at_ms bigint CHECK (revoked_at_ms BETWEEN 0 AND 9007199254740991),
    CHECK ((state = 'revoked') = (revoked_at_ms IS NOT NULL)),
    FOREIGN KEY (environment, receiver_id, binding_version, device_id, provider) REFERENCES observer_bindings(environment, receiver_id, binding_version, device_id, provider) ON DELETE RESTRICT,
    FOREIGN KEY (environment, source_class) REFERENCES observer_environments(environment, source_class) ON DELETE RESTRICT,
    UNIQUE (environment, key_id),
    UNIQUE (environment, device_id, key_id)
);
CREATE TABLE observer_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    environment varchar(64) NOT NULL,
    device_id varchar(64) NOT NULL,
    event_id text NOT NULL CHECK (event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    receiver_id varchar(64) NOT NULL,
    binding_version integer NOT NULL,
    provider text NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor BETWEEN 1 AND 999999999999),
    currency text NOT NULL CHECK (currency = 'CNY'),
    observed_at_ms bigint NOT NULL CHECK (observed_at_ms BETWEEN 0 AND 9007199254740991),
    parser_version varchar(64) NOT NULL CHECK (parser_version ~ '^[A-Za-z0-9._-]{1,64}$'),
    source_class text NOT NULL,
    identity_confidence text NOT NULL CHECK (identity_confidence IN ('distinct', 'uncertain')),
    provider_transaction_id text CHECK (char_length(provider_transaction_id) BETWEEN 1 AND 128 AND provider_transaction_id !~ '[[:cntrl:]]' AND provider_transaction_id <> event_id),
    body bytea NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 16384),
    body_sha256 text NOT NULL CHECK (body_sha256 ~ '^[0-9a-f]{64}$' AND body_sha256 = encode(sha256(body), 'hex')),
    first_received_at_ms bigint NOT NULL CHECK (first_received_at_ms BETWEEN 0 AND 9007199254740991),
    processing_status text NOT NULL DEFAULT 'pending' CHECK (processing_status IN ('pending', 'needs_review')),
    UNIQUE (environment, device_id, event_id),
    UNIQUE (id, environment, device_id),
    FOREIGN KEY (environment, receiver_id, binding_version, device_id, provider) REFERENCES observer_bindings(environment, receiver_id, binding_version, device_id, provider) ON DELETE RESTRICT,
    FOREIGN KEY (environment, source_class) REFERENCES observer_environments(environment, source_class) ON DELETE RESTRICT
);
CREATE TABLE observer_event_conflicts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_pk bigint NOT NULL,
    environment varchar(64) NOT NULL,
    device_id varchar(64) NOT NULL,
    key_id varchar(64) NOT NULL,
    request_id text NOT NULL CHECK (request_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    incoming_body_sha256 text NOT NULL CHECK (incoming_body_sha256 ~ '^[0-9a-f]{64}$'),
    first_seen_at_ms bigint NOT NULL CHECK (first_seen_at_ms BETWEEN 0 AND 9007199254740991),
    UNIQUE (event_pk, incoming_body_sha256),
    FOREIGN KEY (event_pk, environment, device_id) REFERENCES observer_events(id, environment, device_id) ON DELETE RESTRICT,
    FOREIGN KEY (environment, device_id, key_id) REFERENCES observer_credentials(environment, device_id, key_id) ON DELETE RESTRICT
);
CREATE FUNCTION observer_check_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM channels WHERE id = NEW.channel_id AND plugin = 'epay' AND deleted_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_channel_scope'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_channel_scope BEFORE INSERT ON observer_channel_aliases FOR EACH ROW EXECUTE FUNCTION observer_check_channel();
CREATE FUNCTION observer_keep_channel_plugin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.plugin <> 'epay' AND EXISTS (SELECT 1 FROM observer_channel_aliases WHERE channel_id = OLD.id) THEN RAISE EXCEPTION 'observer_channel_scope'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_channel_plugin BEFORE UPDATE OF plugin ON channels FOR EACH ROW EXECUTE FUNCTION observer_keep_channel_plugin();

CREATE FUNCTION observer_next_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE latest integer;
BEGIN
    PERFORM 1 FROM observer_receivers WHERE environment = NEW.environment AND receiver_id = NEW.receiver_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_receiver_missing'; END IF;
    SELECT max(binding_version) INTO latest FROM observer_bindings WHERE environment = NEW.environment AND receiver_id = NEW.receiver_id;
    IF NEW.binding_version::bigint <> coalesce(latest::bigint, 0) + 1 THEN
        RAISE EXCEPTION 'observer_binding_generation';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_binding_generation BEFORE INSERT ON observer_bindings FOR EACH ROW EXECUTE FUNCTION observer_next_binding();

CREATE FUNCTION observer_guard_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE allowed text[];
BEGIN
    IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'observer_history_retained'; END IF;
    CASE TG_TABLE_NAME
        WHEN 'observer_environments', 'observer_receivers', 'observer_devices' THEN allowed := ARRAY['enabled'];
        WHEN 'observer_bindings' THEN
            allowed := ARRAY['active'];
            IF NOT OLD.active AND NEW.active THEN RAISE EXCEPTION 'observer_binding_reactivation'; END IF;
        WHEN 'observer_credentials' THEN
            allowed := ARRAY['state', 'expires_at_ms', 'revoked_at_ms'];
            IF NEW.expires_at_ms > OLD.expires_at_ms OR (OLD.state = 'revoked' AND NEW.state <> 'revoked') OR (OLD.state = 'active' AND NEW.state = 'pending') THEN RAISE EXCEPTION 'observer_credential_reactivation'; END IF;
        WHEN 'observer_events' THEN allowed := ARRAY['processing_status'];
        ELSE allowed := ARRAY[]::text[];
    END CASE;
    IF (to_jsonb(NEW) - allowed) IS DISTINCT FROM (to_jsonb(OLD) - allowed) THEN RAISE EXCEPTION 'observer_identity_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_environment_guard BEFORE UPDATE OR DELETE ON observer_environments FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_receiver_guard BEFORE UPDATE OR DELETE ON observer_receivers FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_alias_guard BEFORE UPDATE OR DELETE ON observer_channel_aliases FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_device_guard BEFORE UPDATE OR DELETE ON observer_devices FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_binding_guard BEFORE UPDATE OR DELETE ON observer_bindings FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_credential_guard BEFORE UPDATE OR DELETE ON observer_credentials FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_event_guard BEFORE UPDATE OR DELETE ON observer_events FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TRIGGER observer_conflict_guard BEFORE UPDATE OR DELETE ON observer_event_conflicts FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
