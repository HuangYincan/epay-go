-- No rules or pilots are seeded. Registration has no public HTTP route.
CREATE TABLE observer_notification_rules (
    parser_version varchar(64) PRIMARY KEY CHECK (parser_version ~ '^[A-Za-z0-9._-]{1,64}$'),
    provider text NOT NULL CHECK (provider='alipay'),
    evidence_ref varchar(64) NOT NULL CHECK (evidence_ref ~ '^[A-Za-z0-9_-]{1,64}$'),
    approved_by bigint NOT NULL REFERENCES admins(id),
    enabled boolean NOT NULL DEFAULT false
);
CREATE TRIGGER observer_notification_rule_guard BEFORE UPDATE OR DELETE ON observer_notification_rules FOR EACH ROW EXECUTE FUNCTION observer_guard_synthetic_merchant();
CREATE TABLE observer_notification_pilots (
    order_id bigint PRIMARY KEY REFERENCES orders(id),
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    provider text NOT NULL CHECK (provider='alipay'),
    device_id varchar(64) NOT NULL,
    binding_version integer NOT NULL,
    channel_id bigint NOT NULL REFERENCES channels(id),
    merchant_id bigint NOT NULL REFERENCES merchants(id),
    amount_minor bigint NOT NULL CHECK (amount_minor BETWEEN 1 AND 999999999999),
    parser_version varchar(64) NOT NULL REFERENCES observer_notification_rules(parser_version),
    armed_at_ms bigint NOT NULL CHECK (armed_at_ms BETWEEN 0 AND 9007199254140991),
    expires_at_ms bigint NOT NULL CHECK (expires_at_ms>armed_at_ms AND expires_at_ms<=armed_at_ms+600000),
    actor_id bigint NOT NULL REFERENCES admins(id),
    enabled boolean NOT NULL DEFAULT true,
    UNIQUE(environment,receiver_id,provider,amount_minor),
    UNIQUE(channel_id,provider,amount_minor),
    FOREIGN KEY(environment,receiver_id,binding_version,device_id,provider) REFERENCES observer_bindings(environment,receiver_id,binding_version,device_id,provider)
);
CREATE TRIGGER observer_notification_pilot_guard BEFORE UPDATE OR DELETE ON observer_notification_pilots FOR EACH ROW EXECUTE FUNCTION observer_guard_synthetic_merchant();

-- Serializes registration with order creation without taking receiver locks in reverse order.
-- A different order cannot acquire a permanently reserved trial amount.
CREATE FUNCTION observer_pilot_order_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND ROW(NEW.channel_id,NEW.pay_type,NEW.amount) IS NOT DISTINCT FROM ROW(OLD.channel_id,OLD.pay_type,OLD.amount) THEN RETURN NEW; END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('observer-pilot-channel:' || NEW.channel_id::text,0));
    IF EXISTS(SELECT 1 FROM observer_notification_pilots p WHERE p.channel_id=NEW.channel_id AND p.provider=NEW.pay_type AND p.amount_minor=NEW.amount*100 AND p.order_id<>NEW.id)
    THEN RAISE EXCEPTION 'observer_pilot_amount_reserved'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_pilot_order_insert BEFORE INSERT OR UPDATE OF channel_id,pay_type,amount ON orders FOR EACH ROW EXECUTE FUNCTION observer_pilot_order_insert();
CREATE FUNCTION observer_pilot_registration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM observer_environments WHERE environment=NEW.environment AND source_class='notification' AND enabled FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_environment'; END IF;
    PERFORM 1 FROM observer_receivers WHERE environment=NEW.environment AND receiver_id=NEW.receiver_id AND provider=NEW.provider AND enabled FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_receiver'; END IF;
    PERFORM 1 FROM observer_credentials WHERE environment=NEW.environment AND receiver_id=NEW.receiver_id
      AND provider=NEW.provider AND device_id=NEW.device_id AND binding_version=NEW.binding_version
      AND source_class='notification' AND state='active' AND not_before_ms<=NEW.armed_at_ms AND expires_at_ms>=NEW.expires_at_ms FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_credential'; END IF;
    PERFORM 1 FROM channels WHERE id=NEW.channel_id AND plugin='epay' AND status=1 AND deleted_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_channel'; END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('observer-pilot-channel:' || NEW.channel_id::text,0));
    IF EXISTS(SELECT 1 FROM observer_notification_pilots WHERE channel_id=NEW.channel_id AND ROW(environment,receiver_id,provider) IS DISTINCT FROM ROW(NEW.environment,NEW.receiver_id,NEW.provider)) THEN RAISE EXCEPTION 'observer_pilot_channel_receiver'; END IF;
    IF EXISTS(SELECT 1 FROM observer_notification_pilots WHERE environment=NEW.environment AND receiver_id=NEW.receiver_id AND enabled AND expires_at_ms>NEW.armed_at_ms)
    THEN RAISE EXCEPTION 'observer_pilot_already_active'; END IF;
    IF EXISTS(SELECT 1 FROM observer_events WHERE environment=NEW.environment AND receiver_id=NEW.receiver_id AND provider=NEW.provider AND amount_minor=NEW.amount_minor)
    THEN RAISE EXCEPTION 'observer_pilot_earlier_evidence'; END IF;
    IF EXISTS(SELECT 1 FROM observer_channel_aliases WHERE channel_id=NEW.channel_id)
    THEN RAISE EXCEPTION 'observer_pilot_alias_conflict'; END IF;
    PERFORM 1 FROM observer_notification_rules WHERE parser_version=NEW.parser_version AND provider=NEW.provider AND enabled FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_rule'; END IF;
    PERFORM 1 FROM observer_bindings b JOIN observer_devices d USING(environment,device_id)
      WHERE b.environment=NEW.environment AND b.receiver_id=NEW.receiver_id AND b.provider=NEW.provider
      AND b.device_id=NEW.device_id AND b.binding_version=NEW.binding_version AND b.active AND d.enabled FOR SHARE OF b,d;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_binding'; END IF;
    PERFORM 1 FROM orders WHERE id=NEW.order_id AND channel_id=NEW.channel_id AND merchant_id=NEW.merchant_id
      AND pay_type=NEW.provider AND amount*100=NEW.amount_minor AND real_amount=amount
      AND status=0 AND deleted_at IS NULL AND observer_pay_expires_at IS NULL
      AND notify_url<>'' AND checkout_type='qrcode' AND pay_url<>''
      AND created_at<=to_timestamp(NEW.armed_at_ms::numeric/1000)
      AND (checkout_expires_at IS NULL OR checkout_expires_at>=to_timestamp(NEW.expires_at_ms::numeric/1000)) FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_order'; END IF;
    PERFORM 1 FROM merchants WHERE id=NEW.merchant_id AND status=1 AND deleted_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_merchant'; END IF;
    IF EXISTS(SELECT 1 FROM orders WHERE channel_id=NEW.channel_id AND pay_type=NEW.provider AND amount*100=NEW.amount_minor AND id<>NEW.order_id)
    THEN RAISE EXCEPTION 'observer_pilot_history_conflict'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_pilot_registration BEFORE INSERT ON observer_notification_pilots FOR EACH ROW EXECUTE FUNCTION observer_pilot_registration();
CREATE FUNCTION observer_pilot_keep_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM observer_notification_pilots WHERE order_id=OLD.id) AND
      ROW(NEW.id,NEW.trade_no,NEW.out_trade_no,NEW.channel_id,NEW.merchant_id,NEW.pay_type,NEW.amount,NEW.real_amount,NEW.fee,NEW.notify_url,NEW.checkout_type,NEW.pay_url,NEW.pay_params,NEW.created_at,NEW.checkout_expires_at,NEW.observer_pay_expires_at)
      IS DISTINCT FROM ROW(OLD.id,OLD.trade_no,OLD.out_trade_no,OLD.channel_id,OLD.merchant_id,OLD.pay_type,OLD.amount,OLD.real_amount,OLD.fee,OLD.notify_url,OLD.checkout_type,OLD.pay_url,OLD.pay_params,OLD.created_at,OLD.checkout_expires_at,OLD.observer_pay_expires_at)
    THEN RAISE EXCEPTION 'observer_pilot_order_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_pilot_keep_order BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION observer_pilot_keep_order();

ALTER TABLE observer_match_decisions DROP CONSTRAINT observer_match_decisions_policy_version_check;
ALTER TABLE observer_match_decisions ADD CONSTRAINT observer_match_decisions_policy_version_check CHECK(policy_version IN ('synthetic-exclusive-v1','notification-review-v1','notification-pilot-v1'));
CREATE OR REPLACE FUNCTION observer_check_decision_source() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source text;
BEGIN
    SELECT source_class INTO source FROM observer_events WHERE id=NEW.event_pk;
    IF (NEW.policy_version IN ('notification-review-v1','notification-pilot-v1')) IS DISTINCT FROM (source='notification')
    THEN RAISE EXCEPTION 'observer_decision_source'; END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION observer_check_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_key bigint; decision text; state text;
BEGIN
    IF TG_TABLE_NAME='observer_events' THEN event_key:=NEW.id; ELSE event_key:=NEW.event_pk; END IF;
    SELECT processing_status INTO state FROM observer_events WHERE id=event_key;
    SELECT outcome INTO decision FROM observer_match_decisions WHERE event_pk=event_key;
    IF decision='matched' OR state='credited' THEN
        IF EXISTS(SELECT 1 FROM observer_match_decisions WHERE event_pk=event_key AND policy_version='notification-pilot-v1') THEN
            PERFORM 1 FROM observer_consumptions c JOIN observer_events e ON e.id=c.event_pk
              JOIN observer_match_decisions d ON d.event_pk=e.id AND d.order_id=c.order_id AND d.outcome='matched'
              JOIN observer_candidate_snapshots s ON s.event_pk=e.id AND s.candidate_count=1 AND s.candidate_order_id=c.order_id AND s.claim_order_id IS NULL
              JOIN observer_notification_pilots p ON p.order_id=c.order_id AND p.environment=e.environment AND p.receiver_id=e.receiver_id AND p.provider=e.provider
              JOIN orders o ON o.id=p.order_id AND o.channel_id=p.channel_id AND o.merchant_id=p.merchant_id AND o.pay_type=p.provider
              JOIN balance_records l ON l.id=c.ledger_id AND l.merchant_id=o.merchant_id AND l.trade_no=o.trade_no
              WHERE e.id=event_key AND e.source_class='notification' AND e.processing_status='credited' AND e.identity_confidence='distinct'
                AND e.device_id=p.device_id AND e.binding_version=p.binding_version AND e.parser_version=p.parser_version AND e.provider_transaction_id IS NULL
                AND e.amount_minor=p.amount_minor AND o.amount*100=p.amount_minor AND o.real_amount=o.amount AND (o.checkout_expires_at IS NULL OR o.checkout_expires_at>=to_timestamp(p.expires_at_ms::numeric/1000)) AND o.status=1 AND o.api_trade_no IS NULL
                AND e.observed_at_ms>=p.armed_at_ms AND e.first_received_at_ms>=p.armed_at_ms
                AND e.observed_at_ms<p.expires_at_ms AND e.first_received_at_ms<p.expires_at_ms
                AND e.first_received_at_ms-e.observed_at_ms BETWEEN -2000 AND 120000
                AND d.candidate_cutoff_ms=e.first_received_at_ms AND s.candidate_cutoff_ms=e.first_received_at_ms
                AND l.action=1 AND l.type='order_income' AND l.amount=o.amount-o.fee AND l.after_balance-l.before_balance=l.amount;
            IF NOT FOUND THEN RAISE EXCEPTION 'observer_pilot_settlement_incomplete'; END IF;
            RETURN NEW;
        END IF;
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

CREATE FUNCTION observer_pilot_order_paid() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status=1 AND EXISTS(SELECT 1 FROM observer_notification_pilots WHERE order_id=NEW.id) THEN
        IF NOT EXISTS(SELECT 1 FROM observer_consumptions WHERE order_id=NEW.id) THEN RAISE EXCEPTION 'observer_pilot_consumption_required'; END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER observer_pilot_order_paid AFTER UPDATE ON orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observer_pilot_order_paid();

CREATE FUNCTION observer_pilot_alias_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('observer-pilot-channel:' || NEW.channel_id::text,0));
    IF EXISTS(SELECT 1 FROM observer_notification_pilots WHERE channel_id=NEW.channel_id)
    THEN RAISE EXCEPTION 'observer_pilot_alias_conflict'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_pilot_alias_guard BEFORE INSERT ON observer_channel_aliases FOR EACH ROW EXECUTE FUNCTION observer_pilot_alias_guard();
