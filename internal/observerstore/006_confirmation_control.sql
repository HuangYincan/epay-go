ALTER TABLE observer_match_decisions DROP CONSTRAINT observer_match_decisions_reason_check;
ALTER TABLE observer_match_decisions ADD CONSTRAINT observer_match_decisions_reason_check CHECK (reason IN ('eligible','no_candidate','multiple_candidates','window_expired','clock_skew','observed_before_order','stale_observation','order_settled','scope_changed','unqualified_evidence','event_conflict','missing_receipt','missing_snapshot','candidate_changed','claim_missing','preexisting_review','earlier_evidence','confirmation_paused'));
CREATE TABLE observer_confirmation_operations (
    id bigserial PRIMARY KEY,
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    actor_id bigint NOT NULL REFERENCES admins(id),
    paused boolean NOT NULL,
    reason_code text NOT NULL CHECK(reason_code IN ('incident','maintenance','review_completed')),
    operated_at_ms bigint NOT NULL CHECK(operated_at_ms BETWEEN 0 AND 9007199254740991),
    FOREIGN KEY(environment,receiver_id) REFERENCES observer_receivers(environment,receiver_id)
);
CREATE INDEX observer_confirmation_operations_scope ON observer_confirmation_operations(environment,receiver_id,id DESC);
CREATE TRIGGER observer_confirmation_operation_guard BEFORE UPDATE OR DELETE ON observer_confirmation_operations FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TABLE observer_confirmation_controls (
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    paused boolean NOT NULL,
    last_operation_id bigint NOT NULL REFERENCES observer_confirmation_operations(id),
    PRIMARY KEY(environment,receiver_id),
    FOREIGN KEY(environment,receiver_id) REFERENCES observer_receivers(environment,receiver_id)
);
CREATE FUNCTION observer_check_confirmation_control() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'observer_history_retained'; END IF;
    IF TG_OP='UPDATE' AND (ROW(NEW.environment,NEW.receiver_id) IS DISTINCT FROM ROW(OLD.environment,OLD.receiver_id) OR NEW.last_operation_id<=OLD.last_operation_id)
      THEN RAISE EXCEPTION 'observer_control_audit_required'; END IF;
    PERFORM 1 FROM observer_confirmation_operations WHERE id=NEW.last_operation_id AND environment=NEW.environment AND receiver_id=NEW.receiver_id AND paused=NEW.paused;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_control_audit_required'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_confirmation_control_guard BEFORE INSERT OR UPDATE OR DELETE ON observer_confirmation_controls FOR EACH ROW EXECUTE FUNCTION observer_check_confirmation_control();
