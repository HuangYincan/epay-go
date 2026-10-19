package observerstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"gorm.io/gorm"
)

func preflightInput(f *notificationFixture, o *model.Order) observerstore.PilotPreflightInput {
	return observerstore.PilotPreflightInput{Environment: "prod", ReceiverID: f.enrollment.ReceiverID, ParserVersion: pilotTestRule,
		OrderID: o.ID, ActorID: f.actor, NowMs: pairingNow + 1000, ExpiresAtMs: pairingNow + 301000}
}

func TestNotificationPreflightReadOnlyAndNoRegistration(t *testing.T) {
	f, o := preparePilot(t)
	queries := 0
	if err := f.db.Callback().Row().Before("gorm:row").Register("test:require_read_only", func(tx *gorm.DB) {
		queries++
		transaction, ok := tx.Statement.ConnPool.(*sql.Tx)
		if !ok {
			t.Error("preflight query outside transaction")
			return
		}
		var readonly, isolation string
		if err := transaction.QueryRowContext(context.Background(), `SELECT current_setting('transaction_read_only'),current_setting('transaction_isolation')`).Scan(&readonly, &isolation); err != nil || readonly != "on" || isolation != "repeatable read" {
			t.Error("preflight transaction is not read-only repeatable read")
		}
	}); err != nil {
		t.Fatal(err)
	}
	r, err := observerstore.InspectNotificationPilot(context.Background(), f.db, preflightInput(f, o))
	f.db.Callback().Row().Remove("test:require_read_only")
	if err != nil || !r.Ready || len(r.Blockers) != 0 || queries < 10 {
		t.Fatal(r, err, queries)
	}
	for _, table := range []string{"observer_notification_pilots", "observer_events", "observer_requests", "observer_consumptions", "balance_records"} {
		if count(t, f.db, table) != 0 {
			t.Fatal("preflight wrote", table)
		}
	}
	var current model.Order
	if err := f.db.First(&current, o.ID).Error; err != nil || current.Status != model.OrderStatusUnpaid || current.NotifyCount != 0 {
		t.Fatal("preflight changed order", err)
	}
	// Advisory success did not reserve an amount or create payment authority.
	armPilot(t, f, o)
}

func TestNotificationPreflightBlockers(t *testing.T) {
	for _, name := range []string{"rule_unverified", "amount_history_conflict", "earlier_evidence", "alias_conflict", "pilot_conflict", "confirmation_paused", "credential_unavailable", "binding_unavailable", "order_unavailable", "channel_unavailable", "merchant_unavailable", "actor_unavailable", "environment_unavailable"} {
		t.Run(name, func(t *testing.T) {
			f, o := preparePilot(t)
			in := preflightInput(f, o)
			switch name {
			case "rule_unverified":
				in.ParserVersion = "alipay-12.12.30-receipt-v1"
			case "amount_history_conflict":
				h := f.order(t, "synthetic-history", pairingNow-1000, nil, model.OrderStatusPaid)
				f.db.Delete(h)
			case "earlier_evidence":
				pilotReceive(t, f, 1, 1, pairingNow, pairingNow, nil)
			case "alias_conflict":
				f.link(t)
			case "pilot_conflict":
				armPilot(t, f, o)
			case "confirmation_paused":
				if _, err := f.svc.SetConfirmationPaused(context.Background(), f.enrollment.ReceiverID, true, "incident", f.actor, pairingNow); err != nil {
					t.Fatal(err)
				}
			case "credential_unavailable":
				exec(t, f.db, `UPDATE observer_credentials SET state='revoked',revoked_at_ms=1`)
			case "binding_unavailable":
				exec(t, f.db, `UPDATE observer_devices SET enabled=false`)
			case "order_unavailable":
				exec(t, f.db, `UPDATE orders SET checkout_expires_at=? WHERE id=?`, time.UnixMilli(pairingNow+200000), o.ID)
			case "channel_unavailable":
				exec(t, f.db, `UPDATE channels SET status=0`)
			case "merchant_unavailable":
				exec(t, f.db, `UPDATE merchants SET status=0`)
			case "actor_unavailable":
				in.ActorID += 999
			case "environment_unavailable":
				exec(t, f.db, `UPDATE observer_environments SET enabled=false WHERE environment='prod'`)
			}
			r, err := observerstore.InspectNotificationPilot(context.Background(), f.db, in)
			if err != nil || r.Ready {
				t.Fatal(r, err)
			}
			found := false
			for _, code := range r.Blockers {
				found = found || code == name
			}
			if !found {
				t.Fatal("missing blocker", name, r)
			}
		})
	}
}

func TestNotificationPreflightMissingSchemaAndStorageFailure(t *testing.T) {
	db := testDB(t)
	in := observerstore.PilotPreflightInput{Environment: "prod", ReceiverID: "receiver", ParserVersion: "alipay-v1", OrderID: 1, ActorID: 1, NowMs: 1, ExpiresAtMs: 2}
	r, err := observerstore.InspectNotificationPilot(context.Background(), db, in)
	if err != nil || r.Ready || len(r.Blockers) != 1 || r.Blockers[0] != "schema_not_ready" {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err = observerstore.InspectNotificationPilot(ctx, db, in)
	if !errors.Is(err, observerstore.ErrStorage) || r.Ready || len(r.Blockers) != 0 {
		t.Fatal(r, err)
	}
	in.ExpiresAtMs = in.NowMs + 600001
	if _, err := observerstore.InspectNotificationPilot(context.Background(), db, in); !errors.Is(err, observerstore.ErrInput) {
		t.Fatal(err)
	}
}
