package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/example/epay-go/internal/payment"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example/epay-go/internal/config"
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/pkg/jwt"
	"github.com/example/epay-go/pkg/sign"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func auditDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("EPAY_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("EPAY_TEST_DATABASE_DSN is not set")
	}
	c, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*c)
	schema := fmt.Sprintf("audit_http_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	c.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*c)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() {
		database.DB = previous
		pool.Close()
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	if err = db.AutoMigrate(&model.Admin{}, &model.Merchant{}, &model.Channel{}, &model.Order{}, &model.BalanceRecord{}, &model.Settlement{}, &model.Refund{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMerchantIsolationAndDisabledSession(t *testing.T) {
	db := auditDB(t)
	victim := model.Merchant{Username: "victim", Password: "unused", ApiKey: "dummy-victim-key"}
	requester := model.Merchant{Username: "requester", Password: "unused", ApiKey: "dummy-requester-key"}
	channel := model.Channel{Name: "fixture", Plugin: "wechat", Config: json.RawMessage(`{"private_key":"dummy-channel-secret","api_v3_key":"dummy-api-key"}`)}
	for _, v := range []any{&victim, &requester, &channel} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	order := model.Order{TradeNo: "fixture-order", OutTradeNo: "v1", MerchantID: victim.ID, ChannelID: channel.ID, Amount: decimal.NewFromInt(1)}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	previous := config.Cfg
	config.Cfg = &config.Config{JWT: config.JWTConfig{Secret: "test-only-secret", ExpireHour: 1}}
	t.Cleanup(func() { config.Cfg = previous })
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Setup(engine)
	values := url.Values{"pid": {fmt.Sprint(requester.ID)}, "trade_no": {order.TradeNo}}
	values.Set("sign", sign.GenerateMD5Sign(values, requester.ApiKey))
	res := httptest.NewRecorder()
	engine.ServeHTTP(res, httptest.NewRequest("GET", "/api/pay/query?"+values.Encode(), nil))
	if res.Code != 404 || strings.Contains(res.Body.String(), victim.ApiKey) {
		t.Fatalf("cross merchant query: %s", res.Body.String())
	}
	res = httptest.NewRecorder()
	legacy := url.Values{"act": {"order"}, "pid": {fmt.Sprint(requester.ID)}, "key": {requester.ApiKey}, "trade_no": {order.TradeNo}}
	engine.ServeHTTP(res, httptest.NewRequest("GET", "/api.php?"+legacy.Encode(), nil))
	if strings.Contains(res.Body.String(), `"code":1`) {
		t.Fatal("legacy cross merchant query succeeded")
	}
	token, err := jwt.GenerateToken(victim.ID, victim.Username, jwt.TokenTypeMerchant)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/merchant/orders", "/api/merchant/orders/" + order.TradeNo} {
		res = httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		engine.ServeHTTP(res, req)
		if res.Code != 200 || strings.Contains(res.Body.String(), "dummy-channel-secret") || strings.Contains(res.Body.String(), victim.ApiKey) {
			t.Fatalf("secret disclosure %s", res.Body.String())
		}
	}
	res = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/merchant/profile", strings.NewReader(`{"email":"new@example.com","phone":"123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(res, req)
	db.First(&victim, victim.ID)
	if res.Code != 200 || victim.Email != "new@example.com" {
		t.Fatalf("profile not saved: %s", res.Body.String())
	}
	db.Model(&victim).Update("status", 0)
	res = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/merchant/profile", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	engine.ServeHTTP(res, req)
	if res.Code != 403 {
		t.Fatalf("disabled session returned %d", res.Code)
	}
	res = httptest.NewRecorder()
	engine.ServeHTTP(res, httptest.NewRequest("GET", "/api/cashier/"+order.TradeNo, nil))
	if res.Code != 200 || strings.Contains(res.Body.String(), "dummy-") {
		t.Fatal("cashier missing or discloses secrets")
	}
}

type httpFixtureAdapter struct {
	key     string
	created *[]payment.CreateOrderRequest
}

func (a *httpFixtureAdapter) CreateOrder(_ context.Context, r *payment.CreateOrderRequest) (*payment.CreateOrderResponse, error) {
	if a.created != nil {
		*a.created = append(*a.created, *r)
	}
	return &payment.CreateOrderResponse{PayType: "jsapi", PayParams: `{"appId":"fixture"}`}, nil
}
func (a *httpFixtureAdapter) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, errors.New("unused")
}
func (a *httpFixtureAdapter) Refund(context.Context, *payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, errors.New("unused")
}
func (a *httpFixtureAdapter) ParseNotify(_ context.Context, r *http.Request) (*payment.NotifyResult, error) {
	if r.Header.Get("X-Fixture-Signature") != a.key {
		return nil, errors.New("bad fixture signature")
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	amount, err := decimal.NewFromString(r.Form.Get("amount"))
	if err != nil {
		return nil, err
	}
	return &payment.NotifyResult{TradeNo: r.Form.Get("trade_no"), ApiTradeNo: "fixture-provider", Status: "success", Amount: amount}, nil
}
func (a *httpFixtureAdapter) NotifySuccess() string { return "success" }
func TestCallbackChannelSelection(t *testing.T) {
	db := auditDB(t)
	m := model.Merchant{Username: "callback", Password: "unused", ApiKey: "dummy-callback"}
	first := model.Channel{Plugin: "http-fixture", Config: json.RawMessage(`{"key":"first"}`), Status: 1}
	second := model.Channel{Plugin: "http-fixture", Config: json.RawMessage(`{"key":"second"}`), Status: 1}
	for _, v := range []any{&m, &first, &second} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&second).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	o := model.Order{TradeNo: "callback-order", OutTradeNo: "callback-order", MerchantID: m.ID, ChannelID: second.ID, Amount: decimal.NewFromInt(2)}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	payment.Register("http-fixture", func(raw json.RawMessage) (payment.PaymentAdapter, error) {
		var cfg struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		return &httpFixtureAdapter{key: cfg.Key}, nil
	})
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Setup(engine)
	call := func(path, key string) string {
		res := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader("trade_no=callback-order&amount=2"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Fixture-Signature", key)
		engine.ServeHTTP(res, req)
		return res.Body.String()
	}
	if call(fmt.Sprintf("/api/pay/notify/http-fixture/%d", first.ID), "first") != "fail" {
		t.Fatal("callback accepted wrong channel")
	}
	db.First(&o, o.ID)
	if o.Status != 0 {
		t.Fatal("wrong channel credited")
	}
	if call("/api/pay/notify/http-fixture", "second") != "success" {
		t.Fatal("legacy callback failed to find disabled historical channel")
	}
	// Wait for the asynchronous no-URL notification before the fixture schema closes.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		db.First(&o, o.ID)
		if o.NotifyStatus == model.NotifyStatusSuccess {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if o.NotifyStatus != model.NotifyStatusSuccess {
		t.Fatal("notification did not finish")
	}
	db.First(&m, m.ID)
	if o.Status != model.OrderStatusPaid || !m.Balance.Equal(decimal.NewFromInt(2)) {
		t.Fatal("correct callback did not credit exactly once")
	}
}
func TestSignedJSAPIAndCashier(t *testing.T) {
	db := auditDB(t)
	m := model.Merchant{Username: "jsapi", Password: "unused", ApiKey: "dummy-jsapi-key"}
	c := model.Channel{Plugin: "jsapi-fixture", PayTypes: "wxpay", AppType: "jsapi", Config: json.RawMessage(`{}`), Status: 1}
	for _, v := range []any{&m, &c} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	var created []payment.CreateOrderRequest
	payment.Register("jsapi-fixture", func(json.RawMessage) (payment.PaymentAdapter, error) {
		return &httpFixtureAdapter{created: &created}, nil
	})
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Setup(engine)
	for i, path := range []string{"/api/pay/create", "/api/pay/create", "/mapi.php"} {
		values := url.Values{"pid": {fmt.Sprint(m.ID)}, "type": {"wxpay"}, "pay_method": {"jsapi"}, "openid": {"fixture-openid"}, "out_trade_no": {fmt.Sprintf("jsapi-%d", i)}, "notify_url": {"https://8.8.8.8/fixture"}, "name": {"fixture"}, "money": {"1"}}
		values.Set("sign", sign.GenerateMD5Sign(values, m.ApiKey))
		body := values.Encode()
		contentType := "application/x-www-form-urlencoded"
		if i == 1 {
			data := map[string]string{}
			for k, v := range values {
				data[k] = v[0]
			}
			raw, _ := json.Marshal(data)
			body = string(raw)
			contentType = "application/json"
		}
		res := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		engine.ServeHTTP(res, req)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `"pay_params"`) {
			t.Fatalf("signed JSAPI rejected: %s", res.Body.String())
		}
	}
	if len(created) != 3 {
		t.Fatalf("created %d orders", len(created))
	}
	for _, req := range created {
		if req.Extra["openid"] != "fixture-openid" || req.PayMethod != "jsapi" || !strings.HasSuffix(req.NotifyURL, fmt.Sprintf("/jsapi-fixture/%d", c.ID)) {
			t.Fatal("payer or callback not forwarded")
		}
	}
	var o model.Order
	if err := db.Order("id").First(&o).Error; err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/cashier/"+o.TradeNo+"/pay", strings.NewReader(`{"pay_type":"wxpay"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(res, req)
	if len(created) != 3 || !strings.Contains(res.Body.String(), `"pay_params"`) {
		t.Fatal("cashier did not return cached checkout")
	}
}
func TestMigrationDuplicatePreflight(t *testing.T) {
	db := auditDB(t)
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(&model.Order{}, "idx_merchant_order"); err != nil {
		t.Fatal(err)
	}
	m := model.Merchant{Username: "migration", Password: "unused", ApiKey: "dummy-migration"}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	c := model.Channel{Plugin: "fixture", Config: json.RawMessage(`{}`)}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	for _, no := range []string{"migration-one", "migration-two"} {
		o := model.Order{TradeNo: no, OutTradeNo: "duplicate", MerchantID: m.ID, ChannelID: c.ID, Amount: decimal.NewFromInt(1)}
		if err := db.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Migrate(); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatal("duplicate history not reported")
	}
	var n int64
	db.Model(&model.Order{}).Count(&n)
	if n != 2 {
		t.Fatal("migration deleted historical payments")
	}
}
