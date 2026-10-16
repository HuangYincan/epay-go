ALTER TABLE orders ADD COLUMN IF NOT EXISTS observer_pay_expires_at timestamptz;
CREATE TABLE observer_synthetic_merchants (
    merchant_id bigint PRIMARY KEY REFERENCES merchants(id) ON DELETE RESTRICT,
    environment varchar(64) NOT NULL,
    source_class text NOT NULL DEFAULT 'synthetic' CHECK (source_class = 'synthetic'),
    enabled boolean NOT NULL DEFAULT false,
    UNIQUE (environment, merchant_id),
    FOREIGN KEY (environment, source_class) REFERENCES observer_environments(environment, source_class) ON DELETE RESTRICT
);
CREATE FUNCTION observer_guard_synthetic_merchant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN RAISE EXCEPTION 'observer_history_retained'; END IF;
    IF (to_jsonb(NEW) - 'enabled') IS DISTINCT FROM (to_jsonb(OLD) - 'enabled') THEN RAISE EXCEPTION 'observer_identity_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_synthetic_merchant_guard BEFORE UPDATE OR DELETE ON observer_synthetic_merchants FOR EACH ROW EXECUTE FUNCTION observer_guard_synthetic_merchant();
CREATE TABLE observer_amount_claims (
    environment varchar(64) NOT NULL,
    receiver_id varchar(64) NOT NULL,
    provider text NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor BETWEEN 1 AND 999999999999),
    order_id bigint NOT NULL UNIQUE REFERENCES orders(id) ON DELETE RESTRICT,
    channel_id bigint NOT NULL,
    merchant_id bigint NOT NULL,
    created_at_ms bigint NOT NULL CHECK (created_at_ms BETWEEN 0 AND 9007199254140991),
    observer_pay_expires_at_ms bigint NOT NULL CHECK (observer_pay_expires_at_ms = created_at_ms + 600000),
    policy_version text NOT NULL CHECK (policy_version = 'synthetic-exclusive-v1'),
    PRIMARY KEY (environment, receiver_id, provider, amount_minor),
    FOREIGN KEY (environment, receiver_id, provider) REFERENCES observer_receivers(environment, receiver_id, provider) ON DELETE RESTRICT,
    FOREIGN KEY (channel_id, provider) REFERENCES observer_channel_aliases(channel_id, provider) ON DELETE RESTRICT,
    FOREIGN KEY (environment, merchant_id) REFERENCES observer_synthetic_merchants(environment, merchant_id) ON DELETE RESTRICT
);
CREATE FUNCTION observer_check_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM orders o JOIN observer_channel_aliases a ON a.channel_id = o.channel_id AND a.provider = o.pay_type
    WHERE o.id = NEW.order_id AND o.channel_id = NEW.channel_id AND o.merchant_id = NEW.merchant_id
      AND a.environment = NEW.environment AND a.receiver_id = NEW.receiver_id AND a.provider = NEW.provider
      AND o.amount * 100 = NEW.amount_minor AND o.real_amount = o.amount AND o.status = 0
      AND floor(extract(epoch FROM o.created_at) * 1000) = NEW.created_at_ms
      AND floor(extract(epoch FROM o.observer_pay_expires_at) * 1000) = NEW.observer_pay_expires_at_ms;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_claim_scope'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_claim_scope BEFORE INSERT ON observer_amount_claims FOR EACH ROW EXECUTE FUNCTION observer_check_claim();
CREATE TRIGGER observer_claim_guard BEFORE UPDATE OR DELETE ON observer_amount_claims FOR EACH ROW EXECUTE FUNCTION observer_guard_history();
CREATE FUNCTION observer_require_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.observer_pay_expires_at IS NOT NULL OR EXISTS (SELECT 1 FROM observer_channel_aliases WHERE channel_id = NEW.channel_id) THEN
        IF NOT EXISTS (SELECT 1 FROM observer_amount_claims WHERE order_id = NEW.id) THEN RAISE EXCEPTION 'observer_claim_required'; END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER observer_order_claim_required AFTER INSERT ON orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION observer_require_claim();
CREATE FUNCTION observer_guard_order_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.observer_pay_expires_at IS NULL AND NEW.observer_pay_expires_at IS NOT NULL THEN RAISE EXCEPTION 'observer_history_not_upgraded'; END IF;
    IF EXISTS (SELECT 1 FROM observer_amount_claims WHERE order_id = OLD.id) AND
       ROW(NEW.id,NEW.trade_no,NEW.out_trade_no,NEW.channel_id,NEW.merchant_id,NEW.pay_type,NEW.amount,NEW.real_amount,NEW.created_at,NEW.observer_pay_expires_at)
       IS DISTINCT FROM ROW(OLD.id,OLD.trade_no,OLD.out_trade_no,OLD.channel_id,OLD.merchant_id,OLD.pay_type,OLD.amount,OLD.real_amount,OLD.created_at,OLD.observer_pay_expires_at)
    THEN RAISE EXCEPTION 'observer_order_scope_immutable'; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER observer_order_claim_guard BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION observer_guard_order_claim();

-- Alias registration participates in the same receiver serialization as claims.
-- It cannot introduce previously hidden same-amount history behind an existing window.
CREATE OR REPLACE FUNCTION observer_check_channel() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM observer_environments WHERE environment = NEW.environment FOR SHARE;
    PERFORM 1 FROM observer_receivers WHERE environment = NEW.environment AND receiver_id = NEW.receiver_id AND provider = NEW.provider FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_receiver_missing'; END IF;
    PERFORM 1 FROM channels WHERE id = NEW.channel_id AND plugin = 'epay' AND deleted_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'observer_channel_scope'; END IF;
    IF EXISTS (SELECT 1 FROM orders o JOIN observer_amount_claims c ON c.amount_minor = o.amount * 100
        WHERE o.channel_id = NEW.channel_id AND o.pay_type = NEW.provider
          AND c.environment = NEW.environment AND c.receiver_id = NEW.receiver_id AND c.provider = NEW.provider)
    THEN RAISE EXCEPTION 'observer_alias_history_conflict'; END IF;
    RETURN NEW;
END $$;
