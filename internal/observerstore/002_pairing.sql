CREATE TABLE observer_pairing_grants (
    grant_id varchar(64) PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    provider text NOT NULL,
    source_class text NOT NULL,
    origin text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('replace', 'rotate')),
    expected_binding_version integer NOT NULL CHECK (expected_binding_version >= 0),
    target_device_id varchar(64) NOT NULL,
    created_by bigint NOT NULL REFERENCES admins(id),
    created_at_ms bigint NOT NULL CHECK (created_at_ms BETWEEN 0 AND 9007198954740991),
    expires_at_ms bigint NOT NULL CHECK (expires_at_ms = created_at_ms + 300000),
    revoked boolean NOT NULL DEFAULT false,
    enrollment_id text UNIQUE CHECK (enrollment_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    resume_hash bytea CHECK (octet_length(resume_hash) = 32),
    key_id varchar(64) UNIQUE REFERENCES observer_credentials(key_id),
    CHECK ((enrollment_id IS NULL AND resume_hash IS NULL AND key_id IS NULL) OR
           (enrollment_id IS NOT NULL AND resume_hash IS NOT NULL AND key_id IS NOT NULL)),
    CHECK ((mode = 'replace' AND target_device_id = '') OR (mode = 'rotate' AND target_device_id <> '' AND expected_binding_version > 0)),
    FOREIGN KEY (environment, receiver_id, provider) REFERENCES observer_receivers(environment, receiver_id, provider),
    FOREIGN KEY (environment, source_class) REFERENCES observer_environments(environment, source_class)
);
CREATE TABLE observer_pairing_rate (
    token_hash bytea PRIMARY KEY REFERENCES observer_pairing_grants(token_hash),
    window_start_ms bigint NOT NULL,
    attempts integer NOT NULL CHECK (attempts > 0)
);
CREATE FUNCTION observer_guard_grant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'observer_history_retained'; END IF;
    IF (OLD.revoked AND NOT NEW.revoked) OR
       (to_jsonb(OLD) - ARRAY['revoked','enrollment_id','resume_hash','key_id']) IS DISTINCT FROM
       (to_jsonb(NEW) - ARRAY['revoked','enrollment_id','resume_hash','key_id']) OR
       (OLD.enrollment_id IS NOT NULL AND
        (OLD.enrollment_id,OLD.resume_hash,OLD.key_id) IS DISTINCT FROM (NEW.enrollment_id,NEW.resume_hash,NEW.key_id))
    THEN RAISE EXCEPTION 'observer_grant_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_grant_guard BEFORE UPDATE OR DELETE ON observer_pairing_grants FOR EACH ROW EXECUTE FUNCTION observer_guard_grant();
