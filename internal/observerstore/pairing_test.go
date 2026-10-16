package observerstore_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
)

const pairingNow int64 = 1792022400000

func pairingService(t *testing.T, db *gorm.DB) *observerstore.PairingService {
	t.Helper()
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "test", "synthetic", "https://observer.example", cipher)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func pairingDB(t *testing.T) (*gorm.DB, *observerstore.PairingService, int64) {
	t.Helper()
	db := testDB(t)
	seed(t, db)
	exec(t, db, `UPDATE observer_environments SET enabled=true WHERE environment='test'`)
	exec(t, db, `UPDATE observer_receivers SET enabled=true WHERE environment='test'`)
	exec(t, db, `UPDATE observer_devices SET enabled=true WHERE environment='test'`)
	a := model.Admin{Username: "pairing-test", Password: "test-only"}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	return db, pairingService(t, db), a.ID
}
func request() observer.PairingRequest {
	return observer.PairingRequest{SchemaVersion: 1, EnrollmentID: "11111111-1111-4111-8111-111111111111", ResumeSecret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}
}
func grant(t *testing.T, svc *observerstore.PairingService, actor int64, mode string, now int64) string {
	t.Helper()
	_, token, err := svc.CreateGrant(context.Background(), "fixture-receiver-wx", mode, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func TestCipherScopeAndTamper(t *testing.T) {
	material := bytes.Repeat([]byte{42}, 32)
	cipher, err := observerstore.NewKeyCipher("kek-a", material)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(material, make([]byte, 32)) {
		t.Fatal("KEK input retained")
	}
	s := observerstore.CredentialScope{KeyID: "key", Environment: "test", ReceiverID: "receiver", DeviceID: "device", Provider: "wxpay", SourceClass: "synthetic", BindingVersion: 1}
	key := bytes.Repeat([]byte{7}, 32)
	encrypted, err := cipher.Seal(s, key)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := cipher.Open(s, "kek-a", encrypted)
	if err != nil || !bytes.Equal(decrypted, key) {
		t.Fatal("decrypt failed")
	}
	if bytes.Contains(encrypted, key) {
		t.Fatal("plaintext in ciphertext")
	}
	for _, mutate := range []func(*observerstore.CredentialScope){func(s *observerstore.CredentialScope) { s.KeyID = "other" }, func(s *observerstore.CredentialScope) { s.Environment = "other" }, func(s *observerstore.CredentialScope) { s.ReceiverID = "other" }, func(s *observerstore.CredentialScope) { s.DeviceID = "other" }, func(s *observerstore.CredentialScope) { s.Provider = "alipay" }, func(s *observerstore.CredentialScope) { s.SourceClass = "notification" }, func(s *observerstore.CredentialScope) { s.BindingVersion++ }} {
		changed := s
		mutate(&changed)
		if _, err := cipher.Open(changed, "kek-a", encrypted); err == nil {
			t.Fatal("cross-scope decrypt")
		}
	}
	encrypted[20] ^= 1
	if _, err := cipher.Open(s, "kek-a", encrypted); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestPairingConcurrentRedemptionRecoveryActivation(t *testing.T) {
	ctx := context.Background()
	db, svc, actor := pairingDB(t)
	token := grant(t, svc, actor, "replace", pairingNow)
	inspected, err := svc.Inspect(ctx, token, pairingNow)
	if err != nil || inspected.ExpectedBindingVersion != 1 {
		t.Fatal("inspect", err)
	}
	type result struct {
		enrollment observer.Enrollment
		err        error
	}
	ch := make(chan result, 12)
	for i := 0; i < 12; i++ {
		go func() { e, err := svc.Redeem(ctx, token, request(), pairingNow); ch <- result{e, err} }()
	}
	var first observer.Enrollment
	for i := 0; i < 12; i++ {
		r := <-ch
		if r.err != nil {
			t.Fatal(r.err)
		}
		if i == 0 {
			first = r.enrollment
		} else if first != r.enrollment {
			t.Fatal("concurrent redemption changed credential")
		}
	}
	if first.BindingVersion != 2 || first.DeviceID == "fixture-device-a" || first.KeyBase64URL == "" {
		t.Fatal("new generation missing")
	}
	var state string
	db.Raw(`SELECT state FROM observer_credentials WHERE key_id=?`, first.KeyID).Scan(&state)
	if state != "pending" {
		t.Fatal("credential active before client stores key")
	}
	// Recreate the service: no process-local state is required to recover the lost response.
	svc = pairingService(t, db)
	again, err := svc.Redeem(ctx, token, request(), pairingNow+1)
	if err != nil || again != first {
		t.Fatal("response-loss recovery", err)
	}
	wrong := request()
	wrong.ResumeSecret = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	if _, err := svc.Redeem(ctx, token, wrong, pairingNow+2); !errors.Is(err, observerstore.ErrEnrollment) {
		t.Fatal("wrong recovery accepted", err)
	}
	active, err := svc.Activate(ctx, token, request(), pairingNow+3)
	if err != nil || active.KeyBase64URL != "" {
		t.Fatal("activate", err)
	}
	if _, err := svc.Activate(ctx, token, request(), pairingNow+4); err != nil {
		t.Fatal("activation response recovery", err)
	}
	db.Raw(`SELECT state FROM observer_credentials WHERE key_id=?`, first.KeyID).Scan(&state)
	if state != "active" {
		t.Fatal("activation not committed")
	}
	if count(t, db, "observer_devices") != 4 || count(t, db, "observer_credentials") != 2 || count(t, db, "observer_events") != 0 {
		t.Fatal("pairing duplicated identity or created event")
	}
	if _, err := svc.Redeem(ctx, token, request(), pairingNow+300000); !errors.Is(err, observerstore.ErrGrant) {
		t.Fatal("expired grant restored key", err)
	}
}
func TestPairingRotationRevocationAndStaleGrant(t *testing.T) {
	ctx := context.Background()
	db, svc, actor := pairingDB(t)
	firstToken := grant(t, svc, actor, "replace", pairingNow)
	stale := grant(t, svc, actor, "replace", pairingNow)
	first, err := svc.Redeem(ctx, firstToken, request(), pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(ctx, firstToken, request(), pairingNow); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Redeem(ctx, stale, request(), pairingNow); !errors.Is(err, observerstore.ErrBinding) {
		t.Fatal("stale grant changed generation", err)
	}
	rotation := grant(t, svc, actor, "rotate", pairingNow+10)
	req := request()
	req.EnrollmentID = "22222222-2222-4222-8222-222222222222"
	next, err := svc.Redeem(ctx, rotation, req, pairingNow+10)
	if err != nil {
		t.Fatal(err)
	}
	if next.DeviceID != first.DeviceID || next.BindingVersion != first.BindingVersion || next.KeyID == first.KeyID || next.KeyBase64URL == first.KeyBase64URL {
		t.Fatal("rotation changed binding or reused key")
	}
	var state string
	db.Raw(`SELECT state FROM observer_credentials WHERE key_id=?`, first.KeyID).Scan(&state)
	if state != "active" {
		t.Fatal("old key revoked before activation")
	}
	if _, err := svc.Activate(ctx, rotation, req, pairingNow+11); err != nil {
		t.Fatal(err)
	}
	db.Raw(`SELECT state FROM observer_credentials WHERE key_id=?`, first.KeyID).Scan(&state)
	if state != "revoked" {
		t.Fatal("old key survived rotation")
	}
	if _, err := svc.Redeem(ctx, firstToken, request(), pairingNow+12); !errors.Is(err, observerstore.ErrScope) {
		t.Fatal("revoked key recovered", err)
	}
	if err := svc.RevokeBinding(ctx, "fixture-receiver-wx", next.BindingVersion, pairingNow+13); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(ctx, rotation, req, pairingNow+14); !errors.Is(err, observerstore.ErrScope) {
		t.Fatal("revocation bypass", err)
	}
	if _, _, err := svc.CreateGrant(ctx, "fixture-receiver-wx", "rotate", actor, pairingNow+15); !errors.Is(err, observerstore.ErrBinding) {
		t.Fatal("revoked binding rotated", err)
	}
}
func TestPairingRollbackAndGrantRevocation(t *testing.T) {
	ctx := context.Background()
	db, svc, actor := pairingDB(t)
	token := grant(t, svc, actor, "replace", pairingNow)
	exec(t, db, `CREATE FUNCTION reject_pairing_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$`)
	exec(t, db, `CREATE CONSTRAINT TRIGGER pairing_commit_failure AFTER INSERT ON observer_credentials DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_pairing_commit()`)
	if result, err := svc.Redeem(ctx, token, request(), pairingNow); err == nil || result.KeyBase64URL != "" {
		t.Fatal("failed commit returned secret")
	}
	if count(t, db, "observer_devices") != 3 || count(t, db, "observer_credentials") != 1 {
		t.Fatal("failed commit changed identity")
	}
	var active bool
	db.Raw(`SELECT active FROM observer_bindings WHERE receiver_id='fixture-receiver-wx'`).Scan(&active)
	if !active {
		t.Fatal("old binding lost on rollback")
	}
	exec(t, db, `DROP TRIGGER pairing_commit_failure ON observer_credentials`)
	scope, err := svc.Inspect(ctx, token, pairingNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeGrant(ctx, scope.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Redeem(ctx, token, request(), pairingNow); !errors.Is(err, observerstore.ErrGrant) {
		t.Fatal("revoked grant redeemed", err)
	}
}
func TestPairingPersistentRateAndEnvironment(t *testing.T) {
	ctx := context.Background()
	db, svc, actor := pairingDB(t)
	token := grant(t, svc, actor, "replace", pairingNow)
	var wg sync.WaitGroup
	ch := make(chan error, 21)
	for i := 0; i < 21; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.Inspect(ctx, token, pairingNow); ch <- err }()
	}
	wg.Wait()
	close(ch)
	limited := 0
	for err := range ch {
		if errors.Is(err, observerstore.ErrRate) {
			limited++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if limited != 1 {
		t.Fatal("rate not atomic", limited)
	}
	svc = pairingService(t, db)
	if _, err := svc.Inspect(ctx, token, pairingNow-1); !errors.Is(err, observerstore.ErrRate) {
		t.Fatal("clock rollback reset budget", err)
	}
	if _, err := svc.Inspect(ctx, token, pairingNow+60000); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE observer_environments SET enabled=false WHERE environment='test'`)
	if _, err := svc.Redeem(ctx, token, request(), pairingNow+60001); !errors.Is(err, observerstore.ErrScope) {
		t.Fatal("disabled environment redeemed", err)
	}
}
