ALTER TABLE observer_match_decisions DROP CONSTRAINT observer_match_decisions_policy_version_check;
ALTER TABLE observer_match_decisions ADD CONSTRAINT observer_match_decisions_policy_version_check CHECK (policy_version IN ('synthetic-exclusive-v1','notification-review-v1'));
ALTER TABLE observer_match_decisions ADD CONSTRAINT observer_notification_review_no_credit CHECK (policy_version <> 'notification-review-v1' OR outcome <> 'matched');
ALTER TABLE observer_match_decisions DROP CONSTRAINT observer_match_decisions_reason_check;
ALTER TABLE observer_match_decisions ADD CONSTRAINT observer_match_decisions_reason_check CHECK (reason IN ('eligible','no_candidate','multiple_candidates','window_expired','clock_skew','observed_before_order','stale_observation','order_settled','scope_changed','unqualified_evidence','event_conflict','missing_receipt','missing_snapshot','candidate_changed','claim_missing','preexisting_review','earlier_evidence','confirmation_paused','window_unconfigured','confirmation_not_enabled'));

-- Only new decisions are checked. Old evidence and decisions are never rewritten.
CREATE FUNCTION observer_check_decision_source() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source text;
BEGIN
    SELECT source_class INTO source FROM observer_events WHERE id=NEW.event_pk;
    IF (NEW.policy_version='notification-review-v1') IS DISTINCT FROM (source='notification')
        THEN RAISE EXCEPTION 'observer_decision_source'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_decision_source BEFORE INSERT ON observer_match_decisions FOR EACH ROW EXECUTE FUNCTION observer_check_decision_source();
