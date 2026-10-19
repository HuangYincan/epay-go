package observerstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"gorm.io/gorm"
)

// Synthetic field/review inputs over isolated PostgreSQL, never a real Alipay approval.
func operatorFixture(t *testing.T) (*notificationFixture, *model.Order, observerstore.PilotOperatorInput) {
	t.Helper()
	f := newNotificationFixture(t, "alipay", "https://observer.example")
	expiry := time.UnixMilli(pairingNow + 600000)
	o := f.order(t, "operator-synthetic-order", pairingNow, &expiry, 0)
	exec(t, f.db, `UPDATE orders SET notify_url='https://merchant.example/api/user/epay/notify',checkout_type='qrcode',pay_url='https://qr.example/synthetic-only' WHERE id=?`, o.ID)
	return f, o, observerstore.PilotOperatorInput{SchemaVersion: 2, Origin: "https://observer.example", Environment: "prod", ReceiverID: f.enrollment.ReceiverID, OrderID: o.ID, ActorID: f.actor, ExpiresAtMs: pairingNow + 301000, ParserVersion: observerstore.PilotOperatorParserVersion, EvidenceRef: "synthetic-review-only", TemplateVerified: 1, ChatBoundaryVerified: 1, IdentityPolicyApproved: 1, AmountPolicyApproved: 1, MerchantCallbackVerified: 1}
}
func TestPilotOperatorStrictReviewInput(t *testing.T) {
	in := observerstore.PilotOperatorInput{SchemaVersion: 2, Origin: "https://observer.example", Environment: "test", ReceiverID: "receiver", OrderID: 1, ActorID: 1, ExpiresAtMs: 100000, ParserVersion: observerstore.PilotOperatorParserVersion, EvidenceRef: "synthetic", TemplateVerified: 1, ChatBoundaryVerified: 1, IdentityPolicyApproved: 1, AmountPolicyApproved: 1, MerchantCallbackVerified: 1}
	raw, _ := json.Marshal(in)
	base := string(raw)
	for _, bad := range []string{base + "{}", strings.Replace(base, `"schema_version":2`, `"schema_version":2,"schema_version":2`, 1), strings.Replace(base, `"schema_version":2`, `"schema_version":2,"unknown":0`, 1), strings.Replace(base, `"template_verified":1`, `"template_verified":true`, 1), strings.Replace(base, `"template_verified":1`, `"template_verified":2`, 1), strings.Replace(base, `"template_verified":1`, `"template_verified":1.0`, 1), strings.Replace(base, `"actor_id":1`, `"actor_id":1e0`, 1), strings.Replace(base, in.ParserVersion, "unknown", 1), strings.Repeat(" ", 4097)} {
		if _, e := observerstore.DecodePilotOperatorInput([]byte(bad)); e == nil {
			t.Fatal("invalid operator input accepted")
		}
	}
	good, e := observerstore.DecodePilotOperatorInput(raw)
	if e != nil || !good.ReviewComplete() {
		t.Fatal("canonical input rejected")
	}
	for i := 0; i < 5; i++ {
		bad := in
		switch i {
		case 0:
			bad.TemplateVerified = 0
		case 1:
			bad.ChatBoundaryVerified = 0
		case 2:
			bad.IdentityPolicyApproved = 0
		case 3:
			bad.AmountPolicyApproved = 0
		case 4:
			bad.MerchantCallbackVerified = 0
		}
		if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), nil, bad, 1000); e == nil {
			t.Fatal("incomplete review accepted")
		}
	}
}

// Nil DB proves rejected review/version input cannot reach registration or writes.
func TestPilotOperatorRejectsLegacyAndMixedMerchantReview(t *testing.T) {
	_, _, in := operatorFixture(t)
	raw, _ := json.Marshal(in)
	base := string(raw)
	for _, bad := range []string{
		strings.Replace(base, `"schema_version":2`, `"schema_version":1`, 1),
		strings.Replace(base, `"merchant_callback_verified":1`, `"newapi_contract_verified":1`, 1),
		strings.Replace(base, `"merchant_callback_verified":1`, `"merchant_callback_verified":1,"newapi_contract_verified":1`, 1),
		strings.Replace(base, `,"merchant_callback_verified":1`, ``, 1),
	} {
		if _, e := observerstore.DecodePilotOperatorInput([]byte(bad)); e == nil {
			t.Fatal("old/mixed/missing merchant review accepted")
		}
	}
	in.SchemaVersion = 1
	if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), nil, in, pairingNow+1000); e == nil {
		t.Fatal("legacy request reached registration")
	}
}

func TestPilotOperatorMissingMerchantReviewLeavesNoRulesOrFunds(t *testing.T) {
	f, _, in := operatorFixture(t)
	in.MerchantCallbackVerified = 0
	if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+1000); e == nil {
		t.Fatal("unreviewed merchant accepted")
	}
	for _, table := range []string{"observer_notification_rules", "observer_notification_pilots", "balance_records", "observer_consumptions"} {
		if count(t, f.db, table) != 0 {
			t.Fatal("unreviewed merchant wrote", table)
		}
	}
}

func TestPilotOperatorAtomicArmRecoveryAndSignedSyntheticReceipt(t *testing.T) {
	f, o, in := operatorFixture(t)
	handoff, recovered, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+1000)
	if e != nil || recovered {
		t.Fatal("initial preparation failed", e)
	}
	if handoff.DeviceID != f.enrollment.DeviceID || handoff.ReceiverID != in.ReceiverID || handoff.AmountMinor != 1000 || handoff.BindingVersion != 1 || handoff.ServerArmedAtMs != pairingNow+1000 || handoff.ServerExpiresAtMs != in.ExpiresAtMs {
		t.Fatal("handoff mismatch")
	}
	again, recovered, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+2000)
	if e != nil || !recovered || again != handoff {
		t.Fatal("recovery changed original window", e)
	}
	for _, change := range []func(*observerstore.PilotOperatorInput){func(i *observerstore.PilotOperatorInput) { i.ExpiresAtMs++ }, func(i *observerstore.PilotOperatorInput) { i.EvidenceRef = "different" }, func(i *observerstore.PilotOperatorInput) { i.Origin = "https://other.example" }} {
		bad := in
		change(&bad)
		if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, bad, pairingNow+2000); e == nil {
			t.Fatal("scope changed on recovery")
		}
	}
	if count(t, f.db, "observer_notification_rules") != 1 || count(t, f.db, "observer_notification_pilots") != 1 || count(t, f.db, "balance_records") != 0 {
		t.Fatal("unexpected registration side effects")
	}
	got := pilotReceive(t, f, 1, 701, pairingNow+3000, pairingNow+3000, func(b map[string]any) { b["parser_version"] = observerstore.PilotOperatorParserVersion })
	if got.Status != 200 && got.Status != 202 {
		t.Fatal("signed synthetic receipt failed")
	}
	assertPilotCredit(t, f, o)
}
func TestPilotOperatorRollbackAfterRuleInsert(t *testing.T) {
	f, _, in := operatorFixture(t)
	e := f.db.Callback().Create().Before("gorm:create").Register("operator_test_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "observer_notification_pilots" {
			tx.AddError(errors.New("synthetic-write-failure"))
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	defer f.db.Callback().Create().Remove("operator_test_fail")
	if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+1000); e == nil {
		t.Fatal("write failure accepted")
	}
	if count(t, f.db, "observer_notification_rules") != 0 || count(t, f.db, "observer_notification_pilots") != 0 || count(t, f.db, "balance_records") != 0 {
		t.Fatal("partial registration survived rollback")
	}
}
func TestPilotOperatorOriginAndCancelledContextDoNotWrite(t *testing.T) {
	for _, badOrigin := range []bool{true, false} {
		f, _, in := operatorFixture(t)
		ctx := context.Background()
		if badOrigin {
			in.Origin = "https://other.example"
		} else {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if _, _, e := observerstore.PrepareReviewedNotificationPilot(ctx, f.db, in, pairingNow+1000); e == nil {
			t.Fatal("bad scope accepted")
		}
		if count(t, f.db, "observer_notification_rules") != 0 || count(t, f.db, "observer_notification_pilots") != 0 {
			t.Fatal("rejected operation wrote")
		}
	}
}
func TestPilotOperatorConcurrentRecoveryDoesNotRenew(t *testing.T) {
	f, _, in := operatorFixture(t)
	var wg sync.WaitGroup
	results := make(chan observerstore.PilotHandoff, 8)
	failures := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+1000)
			results <- h
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal("concurrent same-scope preparation failed", e)
		}
	}
	var first observerstore.PilotHandoff
	for h := range results {
		if first.SchemaVersion == 0 {
			first = h
		} else if first != h {
			t.Fatal("concurrent recovery changed handoff")
		}
	}
	if count(t, f.db, "observer_notification_rules") != 1 || count(t, f.db, "observer_notification_pilots") != 1 {
		t.Fatal("duplicate registration")
	}
}

func TestPilotOperatorGoldenHandoffMatchesAndroidContract(t *testing.T) {
	bytes, e := os.ReadFile("../../docs/observer/pilot-operator-v2/handoff.json")
	if e != nil {
		t.Fatal(e)
	}
	var h observerstore.PilotHandoff
	if e = json.Unmarshal(bytes, &h); e != nil {
		t.Fatal(e)
	}
	if h.SchemaVersion != 2 || h.SourceClass != "notification" || h.ParserVersion != observerstore.PilotOperatorParserVersion || h.AmountMinor != 1234 || h.ServerArmedAtMs != 100000 || h.ServerExpiresAtMs != 400000 {
		t.Fatal("golden handoff mismatch")
	}
	if strings.Contains(string(bytes), "merchant_callback_verified") || strings.Contains(string(bytes), "newapi_contract_verified") {
		t.Fatal("Android handoff exposed merchant review")
	}
	encoded, e := json.Marshal(h)
	if e != nil {
		t.Fatal(e)
	}
	var want, got map[string]any
	json.Unmarshal(bytes, &want)
	json.Unmarshal(encoded, &got)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("handoff contains extra/missing or changed fields")
	}
}

func TestPilotOperatorActualCLIPrivateOutputAndRecovery(t *testing.T) {
	binary := os.Getenv("OPERATOR_CLI_BINARY")
	if binary == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("operator CLI binary required in CI")
		}
		t.Skip("native operator binary not provided")
	}
	f, _, in := operatorFixture(t)
	now := time.Now().UnixMilli()
	in.ExpiresAtMs = now + 180000
	exec(t, f.db, `UPDATE orders SET checkout_expires_at=to_timestamp(?::numeric/1000) WHERE id=?`, now+300000, in.OrderID)
	var schema string
	if e := f.db.Raw(`SELECT current_schema()`).Scan(&schema).Error; e != nil {
		t.Fatal(e)
	}
	if !regexp.MustCompile(`^observer_test_[0-9]+$`).MatchString(schema) {
		t.Fatal("invalid isolated test schema")
	}
	report, e := observerstore.InspectNotificationPilot(context.Background(), f.db, in.Preflight(now))
	if e != nil || len(report.Blockers) != 1 || report.Blockers[0] != "rule_unverified" {
		t.Fatal("synthetic CLI fixture preflight", report, e)
	}
	d := t.TempDir()
	input := filepath.Join(d, "review.json")
	raw, _ := json.Marshal(in)
	if e := os.WriteFile(input, raw, 0600); e != nil {
		t.Fatal(e)
	}
	readonly := osexec.Command(binary, "-input", input)
	readonly.Env = append(os.Environ(), "EPAY_OPERATOR_DSN="+os.Getenv("EPAY_TEST_DATABASE_DSN"), "PGOPTIONS=-c search_path="+schema)
	summary, e := readonly.CombinedOutput()
	exit, ok := e.(*osexec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatal("default CLI should only report unverified rule")
	}
	var check struct {
		Applied     bool                         `json:"applied"`
		ReviewReady bool                         `json:"review_ready"`
		Preflight   observerstore.PilotPreflight `json:"preflight"`
	}
	if e := json.Unmarshal(summary, &check); e != nil || check.Applied || !check.ReviewReady || len(check.Preflight.Blockers) != 1 || check.Preflight.Blockers[0] != "rule_unverified" {
		t.Fatal("default CLI result mismatch")
	}
	if count(t, f.db, "observer_notification_rules") != 0 || count(t, f.db, "observer_notification_pilots") != 0 {
		t.Fatal("default CLI registered state")
	}
	var original observerstore.PilotHandoff
	for n := 0; n < 2; n++ {
		output := filepath.Join(d, fmt.Sprintf("handoff-%d.json", n))
		cmd := osexec.Command(binary, "-input", input, "-output", output, "-apply")
		cmd.Env = append(os.Environ(), "EPAY_OPERATOR_DSN="+os.Getenv("EPAY_TEST_DATABASE_DSN"), "PGOPTIONS=-c search_path="+schema)
		result, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal("actual CLI failed (synthetic fixture)", string(result))
		}
		var aggregate struct {
			Applied   bool `json:"applied"`
			Recovered bool `json:"recovered"`
			Written   bool `json:"handoff_written"`
		}
		if e := json.Unmarshal(result, &aggregate); e != nil || !aggregate.Applied || !aggregate.Written || aggregate.Recovered != (n == 1) {
			t.Fatal("CLI result mismatch")
		}
		info, e := os.Stat(output)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private output mode")
		}
		body, e := os.ReadFile(output)
		if e != nil {
			t.Fatal(e)
		}
		var h observerstore.PilotHandoff
		if e := json.Unmarshal(body, &h); e != nil || h.ServerExpiresAtMs != in.ExpiresAtMs {
			t.Fatal("CLI handoff invalid")
		}
		if n == 0 {
			original = h
		} else if original != h {
			t.Fatal("CLI recovery renewed or changed scope")
		}
	}
	if count(t, f.db, "observer_notification_rules") != 1 || count(t, f.db, "observer_notification_pilots") != 1 || count(t, f.db, "balance_records") != 0 {
		t.Fatal("CLI side effects exceeded registration")
	}
}

func TestPilotOperatorReadOnlyInspectionIncludesReviewAndOrigin(t *testing.T) {
	f, _, in := operatorFixture(t)
	for _, mode := range []string{"unapproved", "origin"} {
		input := in
		if mode == "unapproved" {
			input.TemplateVerified = 0
		} else {
			input.Origin = "https://other.example"
		}
		report, e := observerstore.InspectPilotOperator(context.Background(), f.db, input, pairingNow+1000)
		if e != nil || report.Ready {
			t.Fatal("invalid operator readiness accepted")
		}
		expected := "review_incomplete"
		if mode == "origin" {
			expected = "pairing_origin_mismatch"
		}
		seen := false
		for _, b := range report.Blockers {
			seen = seen || b == expected
		}
		if !seen {
			t.Fatal("operator-specific blocker missing")
		}
	}
	if count(t, f.db, "observer_notification_rules") != 0 || count(t, f.db, "observer_notification_pilots") != 0 || count(t, f.db, "balance_records") != 0 {
		t.Fatal("inspection wrote business state")
	}
}

func TestPilotOperatorRecoveryRechecksStateAfterAdvisorySnapshot(t *testing.T) {
	for _, change := range []string{"device", "order", "channel", "merchant"} {
		t.Run(change, func(t *testing.T) {
			f, _, in := operatorFixture(t)
			if _, _, e := observerstore.PrepareReviewedNotificationPilot(context.Background(), f.db, in, pairingNow+1000); e != nil {
				t.Fatal(e)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var seen, unblock sync.Once
			defer unblock.Do(func() { close(release) })
			e := f.db.Callback().Row().Before("gorm:row").Register("operator_recovery_interleave", func(tx *gorm.DB) {
				if strings.Contains(tx.Statement.SQL.String(), "SELECT enabled,source_class FROM observer_environments") {
					seen.Do(func() { close(entered); <-release })
				}
			})
			if e != nil {
				t.Fatal(e)
			}
			defer f.db.Callback().Row().Remove("operator_recovery_interleave")
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _, e := observerstore.PrepareReviewedNotificationPilot(ctx, f.db, in, pairingNow+2000)
				done <- e
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("did not reach lock after advisory checks")
			}
			switch change {
			case "device":
				exec(t, f.db, `UPDATE observer_devices SET enabled=false`)
			case "order":
				exec(t, f.db, `UPDATE orders SET deleted_at=now() WHERE id=?`, in.OrderID)
			case "channel":
				exec(t, f.db, `UPDATE channels SET status=0 WHERE id=?`, f.channel.ID)
			case "merchant":
				exec(t, f.db, `UPDATE merchants SET status=0 WHERE id=?`, f.merchant.ID)
			}
			unblock.Do(func() { close(release) })
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("stale handoff recovered after state changed")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("recovery did not terminate")
			}
			if count(t, f.db, "observer_notification_pilots") != 1 || count(t, f.db, "balance_records") != 0 {
				t.Fatal("recovery changed pilot history or funds")
			}
		})
	}
}
