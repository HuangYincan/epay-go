ALTER TABLE observer_events DROP CONSTRAINT observer_events_processing_status_check;
ALTER TABLE observer_events ADD CONSTRAINT observer_events_processing_status_check CHECK (processing_status IN ('pending','needs_review','credited'));
CREATE TABLE observer_candidate_snapshots (
    event_pk bigint PRIMARY KEY REFERENCES observer_events(id),
    claim_order_id bigint REFERENCES orders(id),
    candidate_count bigint NOT NULL CHECK (candidate_count >= 0),
    candidate_order_id bigint REFERENCES orders(id),
    candidate_cutoff_ms bigint NOT NULL CHECK (candidate_cutoff_ms BETWEEN 0 AND 9007199254740991),
    CHECK ((candidate_count=1) = (candidate_order_id IS NOT NULL))
);
CREATE TRIGGER observer_candidate_snapshot_guard BEFORE UPDATE OR DELETE ON observer_candidate_snapshots FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TABLE observer_match_decisions (
    event_pk bigint PRIMARY KEY REFERENCES observer_events(id),
    policy_version text NOT NULL CHECK (policy_version='synthetic-exclusive-v1'),
    outcome text NOT NULL CHECK (outcome IN ('matched','unmatched','ambiguous','expired','clock_error','stale_observation','duplicate_suspected','evidence_insufficient')),
    reason text NOT NULL CHECK (reason IN ('eligible','no_candidate','multiple_candidates','window_expired','clock_skew','observed_before_order','stale_observation','order_settled','scope_changed','unqualified_evidence','event_conflict','missing_receipt','missing_snapshot','candidate_changed','claim_missing','preexisting_review','earlier_evidence')),
    candidate_count bigint NOT NULL CHECK (candidate_count >= 0),
    candidate_cutoff_ms bigint NOT NULL CHECK (candidate_cutoff_ms BETWEEN 0 AND 9007199254740991),
    order_id bigint REFERENCES orders(id),
    decided_at_ms bigint NOT NULL CHECK (decided_at_ms BETWEEN 0 AND 9007199254740991),
    CHECK (outcome <> 'matched' OR (order_id IS NOT NULL AND candidate_count=1 AND reason='eligible')),
    UNIQUE(event_pk,order_id,outcome)
);
CREATE TRIGGER observer_decision_guard BEFORE UPDATE OR DELETE ON observer_match_decisions FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE TABLE observer_consumptions (
    event_pk bigint PRIMARY KEY,
    order_id bigint NOT NULL UNIQUE REFERENCES orders(id),
    outcome text NOT NULL DEFAULT 'matched' CHECK (outcome='matched'),
    ledger_id bigint NOT NULL UNIQUE REFERENCES balance_records(id),
    consumed_at_ms bigint NOT NULL CHECK (consumed_at_ms BETWEEN 0 AND 9007199254740991),
    FOREIGN KEY(event_pk,order_id,outcome) REFERENCES observer_match_decisions(event_pk,order_id,outcome)
);
CREATE TRIGGER observer_consumption_guard BEFORE UPDATE OR DELETE ON observer_consumptions FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE FUNCTION observer_check_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_key bigint; decision text; state text;
BEGIN
    IF TG_TABLE_NAME='observer_events' THEN event_key:=NEW.id; ELSE event_key:=NEW.event_pk; END IF;
    SELECT processing_status INTO state FROM observer_events WHERE id=event_key;
    SELECT outcome INTO decision FROM observer_match_decisions WHERE event_pk=event_key;
    IF decision='matched' OR state='credited' THEN
        PERFORM 1 FROM observer_consumptions c JOIN observer_events e ON e.id=c.event_pk
        JOIN observer_match_decisions d ON d.event_pk=c.event_pk AND d.order_id=c.order_id AND d.outcome='matched'
        JOIN observer_candidate_snapshots s ON s.event_pk=e.id AND s.claim_order_id=c.order_id AND s.candidate_order_id=c.order_id AND s.candidate_count=1
        JOIN observer_amount_claims a ON a.order_id=c.order_id AND a.environment=e.environment AND a.receiver_id=e.receiver_id AND a.provider=e.provider AND a.amount_minor=e.amount_minor
        JOIN orders o ON o.id=c.order_id AND o.merchant_id=a.merchant_id AND o.channel_id=a.channel_id AND o.pay_type=a.provider
        JOIN balance_records l ON l.id=c.ledger_id AND l.merchant_id=o.merchant_id AND l.trade_no=o.trade_no
        WHERE c.event_pk=event_key AND e.processing_status='credited' AND o.status=1 AND o.api_trade_no IS NULL
          AND l.action=1 AND l.type='order_income' AND l.amount=o.amount-o.fee AND l.after_balance-l.before_balance=l.amount
          AND d.candidate_cutoff_ms=e.first_received_at_ms AND s.candidate_cutoff_ms=e.first_received_at_ms;
        IF NOT FOUND THEN RAISE EXCEPTION 'observer_settlement_incomplete'; END IF;
    ELSIF decision IS NOT NULL AND state <> 'needs_review' THEN RAISE EXCEPTION 'observer_review_state';
    END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER observer_decision_complete AFTER INSERT ON observer_match_decisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observer_check_settlement();
CREATE CONSTRAINT TRIGGER observer_consumption_complete AFTER INSERT ON observer_consumptions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observer_check_settlement();
CREATE CONSTRAINT TRIGGER observer_credit_complete AFTER INSERT OR UPDATE ON observer_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observer_check_settlement();
CREATE FUNCTION observer_keep_terminal_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.processing_status <> 'pending' AND NEW.processing_status <> OLD.processing_status THEN RAISE EXCEPTION 'observer_terminal_event'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_event_terminal BEFORE UPDATE ON observer_events FOR EACH ROW EXECUTE FUNCTION observer_keep_terminal_event();
CREATE FUNCTION observer_keep_income() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM observer_consumptions WHERE ledger_id=OLD.id) THEN RAISE EXCEPTION 'observer_income_immutable'; END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_income_guard BEFORE UPDATE OR DELETE ON balance_records FOR EACH ROW EXECUTE FUNCTION observer_keep_income();
CREATE FUNCTION observer_keep_claim_fee() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.fee IS DISTINCT FROM OLD.fee AND EXISTS (SELECT 1 FROM observer_amount_claims WHERE order_id=OLD.id) THEN RAISE EXCEPTION 'observer_fee_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_claim_fee_guard BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION observer_keep_claim_fee();

CREATE TABLE observer_match_retries (
    event_pk bigint PRIMARY KEY REFERENCES observer_events(id),
    attempts integer NOT NULL CHECK (attempts BETWEEN 1 AND 10),
    next_attempt_at_ms bigint NOT NULL CHECK (next_attempt_at_ms BETWEEN 0 AND 9007199254740991)
);
CREATE INDEX observer_pending_events ON observer_events(environment,first_received_at_ms,id) WHERE processing_status='pending';
