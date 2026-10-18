package observerstore_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T, isolation ...string) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("EPAY_TEST_DATABASE_DSN")
	if dsn == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("observer model tests require isolated PostgreSQL in CI")
		}
		t.Skip("EPAY_TEST_DATABASE_DSN not set; PostgreSQL model tests not run")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	schema := fmt.Sprintf("observer_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	cfg.RuntimeParams["statement_timeout"] = "10000"
	if len(isolation) != 0 {
		cfg.RuntimeParams["default_transaction_isolation"] = isolation[0]
	}
	var pool *sql.DB = stdlib.OpenDB(*cfg)
	pool.SetMaxOpenConns(20)
	t.Cleanup(func() { pool.Close(); admin.Exec("DROP SCHEMA " + schema + " CASCADE"); admin.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Admin{}, &model.Merchant{}, &model.Channel{}, &model.Order{}, &model.BalanceRecord{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func exec(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatal(err)
	}
}
func rejected(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if db.Exec(query, args...).Error == nil {
		t.Fatalf("constraint did not reject %s", query)
	}
}
func count(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}
func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := observerstore.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	seedIdentityRows(t, db)
}

func seedIdentityRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec(t, db, `INSERT INTO observer_environments(environment, source_class) VALUES ('test','synthetic'),('prod','notification')`)
	exec(t, db, `INSERT INTO observer_receivers(environment,receiver_id,provider,account_ref) VALUES ('test','fixture-receiver-wx','wxpay','account-wx'),('test','fixture-receiver-ali','alipay','account-ali'),('prod','prod-receiver','wxpay','prod-account')`)
	exec(t, db, `INSERT INTO observer_devices(environment,device_id) VALUES ('test','fixture-device-a'),('test','device-b'),('prod','prod-device')`)
	exec(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES ('test','fixture-receiver-wx',1,'fixture-device-a','wxpay',true),('test','fixture-receiver-ali',1,'fixture-device-a','alipay',true)`)
	// Shape-only synthetic ciphertext slot, NOT a valid encrypted credential.
	exec(t, db, `INSERT INTO observer_credentials(key_id,environment,receiver_id,binding_version,device_id,provider,source_class,encryption_key_id,key_ciphertext,not_before_ms,expires_at_ms) VALUES ('fixture-key-a','test','fixture-receiver-wx',1,'fixture-device-a','wxpay','synthetic','test-kek',?,0,9007199254740991)`, make([]byte, 60))
}
func verified(t *testing.T, mutation func(string) string) *observer.VerifiedRequest {
	t.Helper()
	data, err := os.ReadFile("../../pkg/observer/testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []struct {
			Body string `json:"body_base64"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	body, _ := base64.StdEncoding.DecodeString(file.Vectors[0].Body)
	if mutation != nil {
		body = []byte(mutation(string(body)))
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	digest := sha256.Sum256(body)
	requestID := fmt.Sprintf("%08x-3333-4333-8333-333333333333", uint32(time.Now().UnixNano()))
	input := strings.Join([]string{"epay-observer-v1", "POST", observer.Path, "fixture-key-a", "1792022400000", requestID, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(input))
	r := &http.Request{Method: "POST", URL: &url.URL{Path: observer.Path}, Header: http.Header{
		"Content-Type": {"application/json"}, "X-Observer-Version": {"1"}, "X-Observer-Key-Id": {"fixture-key-a"},
		"X-Observer-Sent-At": {"1792022400000"}, "X-Observer-Request-Id": {requestID}, "X-Observer-Signature": {hex.EncodeToString(mac.Sum(nil))},
	}}
	v, err := observer.VerifyRequest(r, body, "fixture-key-a", key, 1792022400000)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrationPreservesLegacyDataAndRunsConcurrently(t *testing.T) {
	db := testDB(t)
	merchant := model.Merchant{Username: "legacy", Password: "synthetic", ApiKey: "legacy-api-key", Balance: decimal.RequireFromString("12.34")}
	if err := db.Create(&merchant).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "legacy", Plugin: "epay", Config: json.RawMessage(`{"wechat_qr":"wxp://synthetic","notify_key":"old-test-key"}`)}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	order := model.Order{TradeNo: "legacy-order", OutTradeNo: "merchant-order", MerchantID: merchant.ID, ChannelID: channel.ID, Amount: decimal.NewFromInt(10), ApiTradeNo: "real-legacy-reference"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { results <- observerstore.Migrate(context.Background(), db) }()
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "observer_schema_versions") != 8 || count(t, db, "observer_events") != 0 {
		t.Fatal("migration did not remain additive")
	}
	var afterOrder model.Order
	var afterMerchant model.Merchant
	var afterChannel model.Channel
	db.First(&afterOrder, order.ID)
	db.First(&afterMerchant, merchant.ID)
	db.First(&afterChannel, channel.ID)
	if afterOrder.ApiTradeNo != order.ApiTradeNo || afterOrder.TradeNo != order.TradeNo || !afterMerchant.Balance.Equal(merchant.Balance) {
		t.Fatal("legacy funds/identity changed")
	}
	var config map[string]string
	if err := json.Unmarshal(afterChannel.Config, &config); err != nil || config["wechat_qr"] != "wxp://synthetic" || config["notify_key"] != "old-test-key" {
		t.Fatal("legacy channel config changed")
	}
	exec(t, db, `UPDATE observer_schema_versions SET checksum='wrong'`)
	if !errors.Is(observerstore.Migrate(context.Background(), db), observerstore.ErrSchema) {
		t.Fatal("schema drift ignored")
	}
}
func TestMigrationCollisionRollsBackDDLAndUnknownVersionFails(t *testing.T) {
	db := testDB(t)
	exec(t, db, `CREATE TABLE observer_devices (sentinel text)`)
	if observerstore.Migrate(context.Background(), db) == nil {
		t.Fatal("collision accepted")
	}
	if db.Migrator().HasTable("observer_schema_versions") || db.Migrator().HasTable("observer_environments") {
		t.Fatal("partial DDL escaped rollback")
	}
	exec(t, db, `DROP TABLE observer_devices`)
	if err := observerstore.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO observer_schema_versions VALUES (9,'future')`)
	if !errors.Is(observerstore.Migrate(context.Background(), db), observerstore.ErrSchema) {
		t.Fatal("future schema ignored")
	}
}
func TestIdentityScopeHistoryAndActiveUniqueness(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	rejected(t, db, `INSERT INTO observer_receivers(environment,receiver_id,provider,account_ref) VALUES ('test','duplicate','wxpay','account-wx')`)
	rejected(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES ('test','fixture-receiver-wx',2,'device-b','wxpay',true)`)
	rejected(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES ('test','fixture-receiver-wx',2,'prod-device','wxpay',false)`)
	rejected(t, db, `UPDATE observer_receivers SET account_ref='another-account' WHERE receiver_id='fixture-receiver-wx'`)
	rejected(t, db, `UPDATE observer_devices SET environment='prod' WHERE device_id='device-b'`)
	exec(t, db, `INSERT INTO observer_receivers(environment,receiver_id,provider,account_ref) VALUES ('test','another','wxpay','another')`)
	rejected(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES ('test','another',1,'fixture-device-a','wxpay',true)`)
	exec(t, db, `UPDATE observer_bindings SET active=false WHERE provider='wxpay'`)
	exec(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider,active) VALUES ('test','fixture-receiver-wx',2,'device-b','wxpay',true)`)
	rejected(t, db, `UPDATE observer_bindings SET active=true WHERE provider='wxpay' AND binding_version=1`)
	rejected(t, db, `INSERT INTO observer_bindings(environment,receiver_id,binding_version,device_id,provider) VALUES ('test','fixture-receiver-wx',4,'device-b','wxpay')`)
	if count(t, db, "observer_bindings") != 3 {
		t.Fatal("binding history lost")
	}
	channel := model.Channel{Name: "alias", Plugin: "epay"}
	db.Create(&channel)
	exec(t, db, `INSERT INTO observer_channel_aliases VALUES (?,'test','fixture-receiver-wx','wxpay')`, channel.ID)
	rejected(t, db, `INSERT INTO observer_channel_aliases VALUES (?,'prod','prod-receiver','wxpay')`, channel.ID)
	rejected(t, db, `UPDATE channels SET plugin='wechat' WHERE id=?`, channel.ID)
	rejected(t, db, `UPDATE observer_channel_aliases SET receiver_id='another' WHERE channel_id=?`, channel.ID)
	rejected(t, db, `INSERT INTO observer_channel_aliases VALUES (?,'test','fixture-receiver-wx','alipay')`, channel.ID)
	other := model.Channel{Name: "not-epay", Plugin: "wechat"}
	db.Create(&other)
	rejected(t, db, `INSERT INTO observer_channel_aliases VALUES (?,'test','fixture-receiver-wx','wxpay')`, other.ID)
}
func TestCredentialMetadataCannotBeReboundOrContainPlainKey(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	rejected(t, db, `INSERT INTO observer_credentials(key_id,environment,receiver_id,binding_version,device_id,provider,source_class,encryption_key_id,key_ciphertext,not_before_ms,expires_at_ms) VALUES ('plain-key','test','fixture-receiver-wx',1,'fixture-device-a','wxpay','synthetic','test-kek',?,0,9007199254740991)`, make([]byte, 32))
	rejected(t, db, `INSERT INTO observer_credentials(key_id,environment,receiver_id,binding_version,device_id,provider,source_class,encryption_key_id,key_ciphertext,not_before_ms,expires_at_ms) VALUES ('wrong-source','test','fixture-receiver-wx',1,'fixture-device-a','wxpay','notification','test-kek',?,0,9007199254740991)`, make([]byte, 60))
	rejected(t, db, `UPDATE observer_credentials SET receiver_id='fixture-receiver-ali'`)
	rejected(t, db, `UPDATE observer_credentials SET key_ciphertext=?`, make([]byte, 32))
	rejected(t, db, `UPDATE observer_credentials SET encryption_key_id='other-kek'`)
	exec(t, db, `UPDATE observer_credentials SET state='active'`)
	exec(t, db, `UPDATE observer_credentials SET expires_at_ms=2000`)
	rejected(t, db, `UPDATE observer_credentials SET expires_at_ms=3000`)
	exec(t, db, `UPDATE observer_credentials SET state='revoked',revoked_at_ms=1000`)
	rejected(t, db, `UPDATE observer_credentials SET state='active',revoked_at_ms=NULL`)
	rejected(t, db, `UPDATE observer_credentials SET revoked_at_ms=2000`)
	rejected(t, db, `DELETE FROM observer_credentials`)
}
func TestConcurrentEvidenceDuplicatesAndConflictPreserveOriginal(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	v := verified(t, nil)
	var wg sync.WaitGroup
	results := make(chan observerstore.EvidenceResult, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", v, 1792022400000)
			results <- r
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	stored := 0
	var id int64
	for r := range results {
		if r.Disposition == "stored" {
			stored++
		}
		if id != 0 && id != r.EventPK {
			t.Fatal("duplicate changed ID")
		}
		id = r.EventPK
	}
	if stored != 1 || count(t, db, "observer_events") != 1 {
		t.Fatal("concurrent duplicate created extra event")
	}
	fresh := verified(t, nil)
	if fresh.RequestID == v.RequestID {
		t.Fatal("test request must be fresh")
	}
	if r, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", fresh, 1792022402000); err != nil || r.Disposition != "duplicate" {
		t.Fatal("new envelope changed event identity", err)
	}
	changed := verified(t, func(s string) string { return strings.Replace(s, `"amount_minor":1001`, `"amount_minor":1002`, 1) })
	for i := 0; i < 2; i++ {
		r, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", changed, 1792022401000)
		if err != nil || r.Disposition != "conflict" || r.EventPK != id {
			t.Fatal("conflict lost", err)
		}
	}
	if count(t, db, "observer_event_conflicts") != 1 {
		t.Fatal("conflict not deduplicated")
	}
	var row struct {
		Body                  []byte
		AmountMinor           int64
		FirstReceivedAtMs     int64
		ProviderTransactionID *string
	}
	db.Raw(`SELECT body,amount_minor,first_received_at_ms,provider_transaction_id FROM observer_events`).Scan(&row)
	if string(row.Body) != string(v.BodyBytes()) || row.AmountMinor != 1001 || row.FirstReceivedAtMs != 1792022400000 || row.ProviderTransactionID != nil {
		t.Fatal("original evidence overwritten or transaction invented")
	}
	if _, err := observerstore.RecordEvidence(context.Background(), db, "prod", "fixture-key-a", v, 1792022400000); !errors.Is(err, observerstore.ErrScope) {
		t.Fatal("cross-environment evidence accepted")
	}
	rejected(t, db, `UPDATE observer_events SET amount_minor=1002`)
	rejected(t, db, `UPDATE observer_events SET first_received_at_ms=0`)
	rejected(t, db, `DELETE FROM observer_events`)
	exec(t, db, `UPDATE observer_events SET processing_status='needs_review'`)
	if count(t, db, "orders") != 0 || count(t, db, "balance_records") != 0 {
		t.Fatal("evidence caused payment side effects")
	}
}
func TestEvidenceCommitFailureDoesNotReturnSuccessOrPartialAudit(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	v := verified(t, nil)
	exec(t, db, `CREATE FUNCTION reject_observer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit failure'; END $$`)
	exec(t, db, `CREATE CONSTRAINT TRIGGER reject_observer AFTER INSERT ON observer_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_observer()`)
	if r, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", v, 1792022400000); err == nil || r.EventPK != 0 {
		t.Fatal("failed commit reported success")
	}
	if count(t, db, "observer_events") != 0 {
		t.Fatal("event survived aborted transaction")
	}
	exec(t, db, `DROP TRIGGER reject_observer ON observer_events`)
	if _, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", v, 1792022400000); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `CREATE CONSTRAINT TRIGGER reject_observer AFTER INSERT ON observer_event_conflicts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_observer()`)
	changed := verified(t, func(s string) string { return s + " " })
	if _, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", changed, 1792022401000); err == nil {
		t.Fatal("failed audit commit reported conflict success")
	}
	if count(t, db, "observer_events") != 1 || count(t, db, "observer_event_conflicts") != 0 {
		t.Fatal("partial audit escaped rollback")
	}
}

func TestEvidenceDatabaseBoundsAndFirstEnvelopeProvenance(t *testing.T) {
	db := testDB(t)
	seed(t, db)
	v := verified(t, nil)
	if _, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", v, 1792022400000); err != nil {
		t.Fatal(err)
	}
	var first struct {
		FirstKeyID     string
		FirstRequestID string
	}
	db.Raw(`SELECT first_key_id, first_request_id FROM observer_events`).Scan(&first)
	if first.FirstKeyID != "fixture-key-a" || first.FirstRequestID != v.RequestID {
		t.Fatal("first envelope provenance missing")
	}
	// INSERT new rows to exercise constraints, rather than merely the UPDATE guard.
	columns := []string{"environment", "device_id", "event_id", "receiver_id", "binding_version", "provider", "amount_minor", "currency", "observed_at_ms", "parser_version", "source_class", "identity_confidence", "provider_transaction_id", "body", "body_sha256", "first_key_id", "first_request_id", "first_received_at_ms"}
	for column, expression := range map[string]string{
		"amount_minor": "0", "observed_at_ms": "9007199254740992", "currency": "'USD'",
		"body_sha256": "repeat('0',64)", "body": "decode(repeat('00',16385),'hex')",
		"provider_transaction_id": "'22222222-2222-4222-8222-222222222222'",
		"first_request_id":        "'bad-request'", "binding_version": "99",
	} {
		values := append([]string(nil), columns...)
		for i, name := range columns {
			if name == "event_id" {
				values[i] = "'22222222-2222-4222-8222-222222222222'"
			}
			if name == column {
				values[i] = expression
			}
		}
		rejected(t, db, "INSERT INTO observer_events("+strings.Join(columns, ",")+") SELECT "+strings.Join(values, ",")+" FROM observer_events LIMIT 1")
	}
	withTransaction := verified(t, func(s string) string {
		s = strings.Replace(s, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", 1)
		return strings.TrimSuffix(s, "}") + `,"provider_transaction_id":"synthetic-provider-reference"}`
	})
	if _, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", withTransaction, 1792022400000); err != nil {
		t.Fatal(err)
	}
	var txID string
	db.Raw(`SELECT provider_transaction_id FROM observer_events WHERE event_id='22222222-2222-4222-8222-222222222222'`).Scan(&txID)
	if txID != "synthetic-provider-reference" || count(t, db, "observer_events") != 2 {
		t.Fatal("optional provider evidence corrupted")
	}
}

func TestExplicitIsolationAndOuterTransactionRollback(t *testing.T) {
	db := testDB(t, "repeatable read")
	seed(t, db)
	v := verified(t, nil)
	outerFailure := errors.New("synthetic outer rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := observerstore.RecordEvidence(context.Background(), tx, "test", "fixture-key-a", v, 1792022400000); err != nil {
			return err
		}
		return outerFailure
	})
	if !errors.Is(err, outerFailure) || count(t, db, "observer_events") != 0 {
		t.Fatal("nested evidence escaped the future receiving transaction")
	}
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := observerstore.RecordEvidence(context.Background(), db, "test", "fixture-key-a", v, 1792022400000)
			results <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-results; err != nil {
			t.Fatal("storage must choose read committed independently of database default", err)
		}
	}
	if count(t, db, "observer_events") != 1 {
		t.Fatal("isolation change broke event uniqueness")
	}
}
