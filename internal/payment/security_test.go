package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-pay/gopay/pkg/xhttp"
	"github.com/shopspring/decimal"
)

type providerTestTransport func(*http.Request) (*http.Response, error)

func (f providerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func alipayFixture(t *testing.T, publicKey, privateKey string, isProd any) *AlipayAdapter {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"app_id": "fixture-app", "private_key": privateKey, "public_key": publicKey, "is_prod": isProd})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAlipayAdapter(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a.(*AlipayAdapter)
}

func wechatSecurityFixture(t *testing.T) (*WechatAdapter, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	raw, _ := json.Marshal(WechatConfig{AppID: "fixture-app", MchID: "fixture-merchant", APIv3Key: "0123456789abcdef0123456789abcdef", SerialNo: "fixture-serial", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), PlatformSerialNo: "PUB_KEY_ID_fixture", PlatformPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))})
	a, err := NewWechatAdapter(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a.(*WechatAdapter), key
}

func TestProviderRejectsUntrustedTLS(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"PARAM_ERROR","message":"forged refund rejection"}`))
	}))
	defer server.Close()
	private, public := genPEMPair(t)
	t.Run("alipay", func(t *testing.T) {
		a := alipayFixture(t, public, private, true)
		transport := a.client.GetHttpClient().HttpClient.Transport.(*http.Transport)
		transport.Proxy = nil
		dial := transport.DialContext
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, server.Listener.Addr().String())
		}
		if result, err := a.QueryOrder(context.Background(), "fixture-trade"); err == nil || result != nil {
			t.Fatalf("untrusted provider accepted: %+v / %v", result, err)
		}
	})
	t.Run("wechat", func(t *testing.T) {
		a, _ := wechatSecurityFixture(t)
		a.client.SetProxyHost(server.URL)
		if result, err := a.Refund(context.Background(), &RefundRequest{TradeNo: "fixture-trade", RefundNo: "fixture-refund", Amount: decimal.NewFromInt(1), TotalAmount: decimal.NewFromInt(1)}); err == nil || result != nil {
			t.Fatalf("untrusted refund rejection accepted: %+v / %v", result, err)
		}
	})
	if requests.Load() != 0 {
		t.Fatal("untrusted TLS peer received financial requests")
	}
}

func TestAlipayKeyFormatsAndResponseSignatures(t *testing.T) {
	private, public := genPEMPair(t)
	block, _ := pem.Decode([]byte(public))
	rawKey := base64.StdEncoding.EncodeToString(block.Bytes)
	keyBlock, _ := pem.Decode([]byte(private))
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key := parsed.(*rsa.PrivateKey)
	payload := `{"code":"10000","msg":"Success","trade_status":"TRADE_SUCCESS","total_amount":"1.00","trade_no":"provider-trade","out_trade_no":"fixture-trade"}`
	hash := sha256.Sum256([]byte(payload))
	sign, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []struct{ name, key string }{{"console_base64", rawKey}, {"pem", public}} {
		for _, signed := range []bool{false, true} {
			t.Run(format.name+"/signed_"+strconv.FormatBool(signed), func(t *testing.T) {
				a := alipayFixture(t, format.key, private, true)
				a.client.SetHttpClient(xhttp.NewClient().SetTransport(providerTestTransport(func(*http.Request) (*http.Response, error) {
					signature := ""
					if signed {
						signature = base64.StdEncoding.EncodeToString(sign)
					}
					body := `{"alipay_trade_query_response":` + payload + `,"sign":"` + signature + `"}`
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})))
				result, err := a.QueryOrder(context.Background(), "fixture-trade")
				if !signed && err == nil {
					t.Fatal("unsigned payment was accepted")
				}
				if signed && (err != nil || result.Status != "paid" || !result.Amount.Equal(decimal.NewFromInt(1))) {
					t.Fatalf("valid signed payment rejected: %+v / %v", result, err)
				}
			})
		}
	}
	for _, invalid := range []string{"", "not-a-public-key"} {
		raw, _ := json.Marshal(AlipayConfig{AppID: "fixture-app", PrivateKey: private, PublicKey: invalid})
		if _, err := NewAlipayAdapter(raw); err == nil {
			t.Fatal("invalid verification key accepted")
		}
	}
}

func TestAlipayEnvironmentCompatibility(t *testing.T) {
	private, public := genPEMPair(t)
	for _, value := range []any{true, false, "true", "false"} {
		a := alipayFixture(t, public, private, value)
		want := value == true || value == "true"
		if a.client.IsProd != want {
			t.Fatalf("environment %v became production=%v", value, a.client.IsProd)
		}
	}
	for _, field := range GetAlipayConfig().Inputs {
		if field.Key == "is_prod" && field.Type != "boolean" {
			t.Fatal("admin schema does not emit a boolean environment")
		}
	}
}

func TestWechatH5ReturnURLPreserved(t *testing.T) {
	a, key := wechatSecurityFixture(t)
	payload := `{"h5_url":"https://wx.tenpay.com/cgi-bin/mmpayweb-bin/checkmweb?prepay_id=fixture&package=test"}`
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), "fixture-nonce"
	hash := sha256.Sum256([]byte(stamp + "\n" + nonce + "\n" + payload + "\n"))
	sign, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetHttpClient(xhttp.NewClient().SetTransport(providerTestTransport(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Wechatpay-Timestamp", stamp)
		header.Set("Wechatpay-Nonce", nonce)
		header.Set("Wechatpay-Serial", "PUB_KEY_ID_fixture")
		header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(sign))
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})))
	returnURL := "https://merchant.example/return?order=123&signature=a+b%20c#receipt"
	result, err := a.CreateOrder(context.Background(), &CreateOrderRequest{TradeNo: "fixture", Amount: decimal.NewFromInt(1), PayMethod: "h5", ClientIP: "203.0.113.1", ReturnURL: returnURL})
	if err != nil {
		t.Fatal(err)
	}
	parsedURL, err := url.Parse(result.PayURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsedURL.Query().Get("redirect_url") != returnURL || parsedURL.Query().Get("prepay_id") != "fixture" || parsedURL.Query().Get("signature") != "" || parsedURL.Fragment != "" {
		t.Fatalf("redirect URL was corrupted: %s", result.PayURL)
	}
}
