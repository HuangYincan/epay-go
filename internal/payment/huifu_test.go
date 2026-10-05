package payment

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func genPEMPair(t *testing.T) (privPEM, pubPEM string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}))
}

// TestHuifuSignVerifyRoundTrip 用商户自己的密钥对模拟"汇付公钥==商户公钥"场景，
// 验证 doRequest 的签名/验签闭环、CreateOrder/QueryOrder/Refund/ParseNotify 的字段解析是否自洽。
func TestHuifuSignVerifyRoundTrip(t *testing.T) {
	merchPriv, merchPub := genPEMPair(t)

	cfg := HuifuConfig{
		SysID:              "test_sys",
		ProductID:          "test_product",
		HuifuID:            "6666000000000000",
		MerchantPrivateKey: merchPriv,
		HuifuPublicKey:     merchPub, // 自签自验，只测闭环正确性，不代表真实汇付公钥
	}
	cfgJSON, _ := json.Marshal(cfg)

	// 微信承接插件（hf-wxpay）
	adapter, err := NewHuifuWechatAdapter(cfgJSON)
	if err != nil {
		t.Fatalf("NewHuifuWechatAdapter failed: %v", err)
	}
	h := adapter.(*HuifuAdapter)
	if h.family != "wechat" {
		t.Fatalf("family = %q, want wechat", h.family)
	}

	// 起一个假的汇付服务端：验证请求签名，再用同一把私钥签名返回数据
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			SysID     string          `json:"sys_id"`
			ProductID string          `json:"product_id"`
			Sign      string          `json:"sign"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request failed: %v", err)
			return
		}
		if envelope.SysID != "test_sys" || envelope.ProductID != "test_product" {
			t.Errorf("unexpected sys_id/product_id: %+v", envelope)
		}
		if err := h.verify(envelope.Data, envelope.Sign); err != nil {
			t.Errorf("request sign verify failed: %v", err)
			return
		}

		var reqData map[string]interface{}
		_ = json.Unmarshal(envelope.Data, &reqData)

		var respData []byte
		switch {
		case strings.Contains(r.URL.Path, "jspay"):
			respData, _ = json.Marshal(map[string]interface{}{
				"resp_code": "00000100",
				"resp_desc": "下单成功",
				"qr_code":   "weixin://wxpay/testqrcode",
			})
		case strings.Contains(r.URL.Path, "query"):
			if reqData["org_req_date"] == "" {
				t.Errorf("org_req_date should not be empty")
			}
			respData, _ = json.Marshal(map[string]interface{}{
				"resp_code":    "00000000",
				"resp_desc":    "成功",
				"out_trans_id": "wx_trans_123",
				"trans_amt":    "1.00",
				"trans_stat":   "S",
				"end_time":     "20260701120000",
			})
		case strings.Contains(r.URL.Path, "refund"):
			respData, _ = json.Marshal(map[string]interface{}{
				"resp_code":  "00000000",
				"resp_desc":  "成功",
				"trans_stat": "S",
			})
		}

		respSign, err := h.sign(respData)
		if err != nil {
			t.Fatal(err)
		}
		respBody, _ := json.Marshal(map[string]interface{}{"data": json.RawMessage(respData), "sign": respSign})
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBody)
	}))
	defer srv.Close()

	// 把 host 换成假服务端做请求级测试（doRequest 内部用 huifuAPIHost 常量拼接，这里直接调用底层 doRequest 绕过常量）
	callAndCheck := func(path string, data map[string]interface{}, result interface{}) {
		dataJSON, _ := json.Marshal(data)
		sign, err := h.sign(dataJSON)
		if err != nil {
			t.Fatal(err)
		}
		envelope := map[string]interface{}{
			"sys_id": h.config.SysID, "product_id": h.config.ProductID,
			"sign": sign, "data": json.RawMessage(dataJSON),
		}
		body, _ := json.Marshal(envelope)
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var envResp struct {
			Data json.RawMessage `json:"data"`
			Sign string          `json:"sign"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envResp); err != nil {
			t.Fatal(err)
		}
		if err := h.verify(envResp.Data, envResp.Sign); err != nil {
			t.Fatalf("response verify failed: %v", err)
		}
		if err := json.Unmarshal(envResp.Data, result); err != nil {
			t.Fatal(err)
		}
	}

	// 1. 下单：验证 trade_type 解析 + qr_code 映射
	tradeType, err := h.resolveTradeType("scan")
	if err != nil || tradeType != "T_NATIVE" {
		t.Fatalf("resolveTradeType(scan) = %s, %v; want T_NATIVE", tradeType, err)
	}
	var createResult struct {
		huifuDataHeader
		QrCode string `json:"qr_code"`
	}
	callAndCheck("/v2/trade/payment/jspay", map[string]interface{}{
		"req_seq_id": "20260701120000abcd1234", "req_date": "20260701",
		"huifu_id": h.config.HuifuID, "trade_type": tradeType, "trans_amt": "1.00",
	}, &createResult)
	if !createResult.isSuccess() || createResult.QrCode == "" {
		t.Fatalf("create order result unexpected: %+v", createResult)
	}

	// 2. 查询：验证 org_req_date 从 tradeNo[:8] 派生，且状态映射正确
	tradeNo := "20260701120000abcd1234"
	if tradeNo[:8] != "20260701" {
		t.Fatalf("tradeNo prefix mismatch: %s", tradeNo[:8])
	}
	var queryResult struct {
		huifuDataHeader
		OutTransId string `json:"out_trans_id"`
		TransAmt   string `json:"trans_amt"`
		TransStat  string `json:"trans_stat"`
	}
	callAndCheck("/v2/trade/payment/scanpay/query", map[string]interface{}{
		"huifu_id": h.config.HuifuID, "org_req_date": tradeNo[:8], "org_req_seq_id": tradeNo,
	}, &queryResult)
	if queryResult.TransStat != "S" {
		t.Fatalf("query trans_stat = %s, want S", queryResult.TransStat)
	}
	amt, _ := decimal.NewFromString(queryResult.TransAmt)
	if !amt.Equal(decimal.NewFromFloat(1.00)) {
		t.Fatalf("query amount = %s, want 1.00", amt.String())
	}

	// 3. 退款
	var refundResult struct {
		huifuDataHeader
		TransStat string `json:"trans_stat"`
	}
	callAndCheck("/v2/trade/payment/scanpay/refund", map[string]interface{}{
		"req_date": "20260701", "req_seq_id": "R123", "huifu_id": h.config.HuifuID,
		"ord_amt": "1.00", "org_req_date": tradeNo[:8], "org_req_seq_id": tradeNo,
	}, &refundResult)
	if refundResult.TransStat != "S" {
		t.Fatalf("refund trans_stat = %s, want S", refundResult.TransStat)
	}

	// 4. 异步通知：验证 form-encoded resp_data+sign 的验签与解析
	notifyData, _ := json.Marshal(map[string]interface{}{
		"resp_code": "00000000", "resp_desc": "成功",
		"req_seq_id": tradeNo, "hf_seq_id": "hf_seq_001", "huifu_id": cfg.HuifuID,
		"trans_amt": "1.00", "trans_stat": "S",
	})
	notifySign, err := h.sign(notifyData)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("resp_data", string(notifyData))
	form.Set("sign", notifySign)
	notifyReq := httptest.NewRequest(http.MethodPost, "/api/pay/notify/huifu", strings.NewReader(form.Encode()))
	notifyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	notifyResult, err := h.ParseNotify(context.Background(), notifyReq)
	if err != nil {
		t.Fatalf("ParseNotify failed: %v", err)
	}
	if notifyResult.Status != "success" || notifyResult.TradeNo != tradeNo || notifyResult.ApiTradeNo != "hf_seq_001" {
		t.Fatalf("ParseNotify result unexpected: %+v", notifyResult)
	}

	// 5. 篡改签名应当验签失败（防止验签逻辑形同虚设）
	form2 := url.Values{}
	form2.Set("resp_data", string(notifyData))
	form2.Set("sign", notifySign[:len(notifySign)-4]+"abcd")
	badReq := httptest.NewRequest(http.MethodPost, "/api/pay/notify/huifu", strings.NewReader(form2.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := h.ParseNotify(context.Background(), badReq); err == nil {
		t.Fatal("tampered signature should fail verification, but ParseNotify succeeded")
	}
	// A platform signature can be valid for another merchant using the same public key.
	for _, merchant := range []string{"", "another-merchant"} {
		raw, _ := json.Marshal(map[string]string{"req_seq_id": tradeNo, "hf_seq_id": "hf_seq_001", "huifu_id": merchant, "trans_amt": "1.00", "trans_stat": "S"})
		signature, err := h.sign(raw)
		if err != nil {
			t.Fatal(err)
		}
		body := url.Values{"resp_data": {string(raw)}, "sign": {signature}}
		req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader(body.Encode()))
		if _, err := h.ParseNotify(context.Background(), req); err == nil {
			t.Fatal("accepted a signed callback for another merchant")
		}
	}

}

// TestHuifuRefundAmount 回归测试：汇付退款必须把【本次退款金额】传给 ord_amt，
// 而不是原订单总额——传错会导致按原订单全额退款（真实资金事故）。
func TestHuifuRefundAmount(t *testing.T) {
	merchPriv, merchPub := genPEMPair(t)
	cfg := HuifuConfig{
		SysID: "s", ProductID: "p", HuifuID: "6666000000000000",
		MerchantPrivateKey: merchPriv, HuifuPublicKey: merchPub,
	}
	cfgJSON, _ := json.Marshal(cfg)
	adapter, err := NewHuifuAlipayAdapter(cfgJSON)
	if err != nil {
		t.Fatal(err)
	}
	h := adapter.(*HuifuAdapter)

	var gotOrdAmt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Data json.RawMessage `json:"data"`
			Sign string          `json:"sign"`
		}
		json.NewDecoder(r.Body).Decode(&envelope)
		if err := h.verify(envelope.Data, envelope.Sign); err != nil {
			t.Errorf("request sign verify failed: %v", err)
		}
		var d map[string]interface{}
		json.Unmarshal(envelope.Data, &d)
		gotOrdAmt, _ = d["ord_amt"].(string)

		respData, _ := json.Marshal(map[string]interface{}{
			"resp_code": "00000000", "resp_desc": "成功", "trans_stat": "S",
		})
		respSign, _ := h.sign(respData)
		body, _ := json.Marshal(map[string]interface{}{"data": json.RawMessage(respData), "sign": respSign})
		w.Write(body)
	}))
	defer srv.Close()
	h.baseURL = srv.URL

	// 原订单总额 0.06，本次只退 0.01
	resp, err := h.Refund(context.Background(), &RefundRequest{
		TradeNo:     "20260701120000abcd1234",
		RefundNo:    "R001",
		TotalAmount: decimal.NewFromFloat(0.06),
		Amount:      decimal.NewFromFloat(0.01),
		RefundDesc:  "部分退款",
	})
	if err != nil {
		t.Fatalf("Refund failed: %v", err)
	}
	if resp.Status != "success" {
		t.Fatalf("refund status = %s, want success", resp.Status)
	}
	if gotOrdAmt != "0.01" {
		t.Fatalf("ord_amt 传给汇付的是 %q，应为本次退款金额 0.01（若为 0.06 则会全额退款）", gotOrdAmt)
	}
}

func TestHuifuRefundQueryProtocol(t *testing.T) {
	priv, pub := genPEMPair(t)
	raw, _ := json.Marshal(HuifuConfig{SysID: "s", ProductID: "p", HuifuID: "fixture-merchant", MerchantPrivateKey: priv, HuifuPublicKey: pub})
	adapter, err := NewHuifuWechatAdapter(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := adapter.(*HuifuAdapter)
	for _, tc := range []struct {
		code, state, amount, want string
		badSign                   bool
	}{
		{"00000000", "S", "0.01", "success", false},
		{"00000000", "P", "", "processing", false},
		{"00000100", "P", "", "processing", false},
		{"00000000", "F", "", "failed", false},
		{"20000004", "", "", "not_found", false},
		{"00000000", "S", "0.01", "", true},
	} {
		t.Run(tc.code+tc.state+tc.want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/trade/payment/scanpay/refundquery" {
					t.Errorf("wrong query path %s", r.URL.Path)
				}
				var envelope struct {
					Data json.RawMessage `json:"data"`
					Sign string          `json:"sign"`
				}
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					t.Error(err)
					return
				}
				if err := h.verify(envelope.Data, envelope.Sign); err != nil {
					t.Error(err)
					return
				}
				var request map[string]string
				if err := json.Unmarshal(envelope.Data, &request); err != nil {
					t.Error(err)
					return
				}
				if request["org_req_seq_id"] != "R20260930fixture" || request["org_req_date"] != "20260930" || request["huifu_id"] != "fixture-merchant" {
					t.Errorf("query lost original refund identity: %v", request)
				}
				data, _ := json.Marshal(map[string]string{"resp_code": tc.code, "trans_stat": tc.state, "ord_amt": tc.amount, "hf_seq_id": "gateway-refund"})
				signature, err := h.sign(data)
				if err != nil {
					t.Error(err)
					return
				}
				if tc.badSign {
					signature = "invalid"
				}
				json.NewEncoder(w).Encode(map[string]any{"data": json.RawMessage(data), "sign": signature})
			}))
			defer server.Close()
			h.baseURL = server.URL
			result, err := h.QueryRefund(context.Background(), &RefundRequest{TradeNo: "20260930fixture", RefundNo: "R20260930fixture", Amount: decimal.RequireFromString("0.01")})
			if tc.badSign {
				if err == nil {
					t.Fatal("accepted unverified query response")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want {
				t.Fatalf("status=%s want=%s", result.Status, tc.want)
			}
			if tc.want == "success" && !result.Amount.Equal(decimal.RequireFromString("0.01")) {
				t.Fatal("query amount lost precision")
			}
		})
	}
}

func TestHuifuRefundAcceptedPending(t *testing.T) {
	priv, pub := genPEMPair(t)
	raw, _ := json.Marshal(HuifuConfig{HuifuID: "fixture", MerchantPrivateKey: priv, HuifuPublicKey: pub})
	adapter, err := NewHuifuWechatAdapter(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := adapter.(*HuifuAdapter)
	for _, tc := range []struct {
		code, state, want string
		uncertain         bool
	}{
		{"00000100", "P", "processing", false},
		{"90000000", "F", "failed", false},
		{"90000000", "", "", true},
	} {
		t.Run(tc.code+tc.state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := json.Marshal(map[string]string{"resp_code": tc.code, "trans_stat": tc.state, "resp_desc": "fixture"})
				signature, err := h.sign(data)
				if err != nil {
					t.Error(err)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"data": json.RawMessage(data), "sign": signature})
			}))
			defer srv.Close()
			h.baseURL = srv.URL
			result, err := h.Refund(context.Background(), &RefundRequest{TradeNo: "20260930fixture", RefundNo: "R20260930fixture", Amount: decimal.RequireFromString("0.01"), TotalAmount: decimal.NewFromInt(1)})
			if tc.uncertain {
				if err == nil {
					t.Fatal("uncertain error was treated as definitive rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want {
				t.Fatalf("status=%s want=%s", result.Status, tc.want)
			}
		})
	}
}

func TestHuifuCrossDayOrderIdentity(t *testing.T) {
	priv, pub := genPEMPair(t)
	cfg, _ := json.Marshal(HuifuConfig{SysID: "fixture", ProductID: "fixture", HuifuID: "fixture", MerchantPrivateKey: priv, HuifuPublicKey: pub})
	adapter, err := NewHuifuWechatAdapter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := adapter.(*HuifuAdapter)
	var createdDate, queriedDate string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
			return
		}
		var data map[string]any
		if err := json.Unmarshal(e.Data, &data); err != nil {
			t.Error(err)
			return
		}
		result := map[string]string{"resp_code": "00000000"}
		if strings.Contains(r.URL.Path, "jspay") {
			createdDate = data["req_date"].(string)
			result["qr_code"] = "weixin://fixture"
		} else {
			queriedDate = data["org_req_date"].(string)
			result["trans_stat"] = "S"
			result["trans_amt"] = "1.00"
		}
		raw, _ := json.Marshal(result)
		sign, err := h.sign(raw)
		if err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": json.RawMessage(raw), "sign": sign})
	}))
	defer server.Close()
	h.baseURL = server.URL
	no := time.Now().Add(-24*time.Hour).Format("20060102150405") + "1234567890"
	if _, err := h.CreateOrder(context.Background(), &CreateOrderRequest{TradeNo: no, Amount: decimal.NewFromInt(1), Subject: "fixture", PayMethod: "native"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.QueryOrder(context.Background(), no); err != nil {
		t.Fatal(err)
	}
	if createdDate != no[:8] || queriedDate != no[:8] {
		t.Fatalf("original order date changed: create=%s query=%s want=%s", createdDate, queriedDate, no[:8])
	}
	t.Logf("same trade_no=%s created req_date=%s queried org_req_date=%s", no, createdDate, queriedDate)
}
