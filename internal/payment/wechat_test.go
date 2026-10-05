package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestWechatNotificationAuthenticity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := WechatConfig{AppID: "fixture-app", MchID: "fixture-merchant", APIv3Key: "0123456789abcdef0123456789abcdef", SerialNo: "merchant-serial", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), PlatformSerialNo: "PUB_KEY_ID_fixture", PlatformPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))}
	configJSON, _ := json.Marshal(cfg)
	adapter, err := NewWechatAdapter(configJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"valid", "unsigned", "invalid_signature", "wrong_merchant", "expired", "missing_amount"} {
		t.Run(test, func(t *testing.T) {
			merchant := cfg.MchID
			if test == "wrong_merchant" {
				merchant = "different"
			}
			payload := map[string]any{"appid": cfg.AppID, "mchid": merchant, "out_trade_no": "test-order", "transaction_id": "provider-trade", "trade_state": "SUCCESS", "amount": map[string]int{"total": 100}, "payer": map[string]string{"openid": "fixture-payer"}}
			if test == "missing_amount" {
				delete(payload, "amount")
			}
			plain, _ := json.Marshal(payload)
			block, _ := aes.NewCipher([]byte(cfg.APIv3Key))
			gcm, _ := cipher.NewGCM(block)
			nonce := "0123456789ab"
			associated := "transaction"
			encrypted := gcm.Seal(nil, []byte(nonce), plain, []byte(associated))
			body, _ := json.Marshal(map[string]any{"event_type": "TRANSACTION.SUCCESS", "resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "nonce": nonce, "associated_data": associated, "ciphertext": base64.StdEncoding.EncodeToString(encrypted)}})
			req := httptest.NewRequest("POST", "/notify", bytes.NewReader(body))
			if test != "unsigned" {
				now := time.Now()
				if test == "expired" {
					now = now.Add(-10 * time.Minute)
				}
				stamp := strconv.FormatInt(now.Unix(), 10)
				headerNonce := "test-header-nonce"
				hash := sha256.Sum256([]byte(stamp + "\n" + headerNonce + "\n" + string(body) + "\n"))
				signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
				if err != nil {
					t.Fatal(err)
				}
				if test == "invalid_signature" {
					signature[0] ^= 0xff
				}
				req.Header.Set("Wechatpay-Timestamp", stamp)
				req.Header.Set("Wechatpay-Nonce", headerNonce)
				req.Header.Set("Wechatpay-Serial", cfg.PlatformSerialNo)
				req.Header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(signature))
			}
			result, err := adapter.ParseNotify(context.Background(), req)
			if test == "valid" {
				if err != nil || result.Status != "success" || result.Amount.String() != "1" {
					t.Fatalf("valid result %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s notification", test)
			}
		})
	}
}
