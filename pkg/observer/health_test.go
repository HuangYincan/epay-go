package observer

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestHealthGoldenStrictBodyAndSeparateDomain(t *testing.T) {
	var vector struct {
		KeyID     string `json:"key_id"`
		KeyHex    string `json:"key_hex"`
		Sent      int64  `json:"sent_at_ms"`
		RequestID string `json:"request_id"`
		Body      string `json:"body_utf8"`
		Signature string `json:"signature_hex"`
	}
	data, err := os.ReadFile("testdata/health.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(vector.KeyHex)
	makeRequest := func(body, path, domain string) *http.Request {
		r, _ := http.NewRequest("POST", "https://health.example"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Observer-Version", "1")
		r.Header.Set("X-Observer-Key-Id", vector.KeyID)
		r.Header.Set("X-Observer-Sent-At", strconv.FormatInt(vector.Sent, 10))
		r.Header.Set("X-Observer-Request-Id", vector.RequestID)
		digest := sha256.Sum256([]byte(body))
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(strings.Join([]string{domain, "POST", path, vector.KeyID, strconv.FormatInt(vector.Sent, 10), vector.RequestID, hex.EncodeToString(digest[:])}, "\n")))
		r.Header.Set("X-Observer-Signature", hex.EncodeToString(mac.Sum(nil)))
		return r
	}
	r := makeRequest(vector.Body, HealthPath, "epay-observer-health-v1")
	if r.Header.Get("X-Observer-Signature") != vector.Signature {
		t.Fatal("shared health signature differs")
	}
	if err := VerifyHealthRequest(r, []byte(vector.Body), vector.KeyID, key, vector.Sent); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"schema_version":1,"schema_version":1}`, `{"schema_version":1,"event_id":"fake"}`, `{"schema_version":1.0}`, `{"schema_version":null}`, `{"schema_version":1}{}`, strings.Repeat(" ", 65)} {
		if err := VerifyHealthRequest(makeRequest(body, HealthPath, "epay-observer-health-v1"), []byte(body), vector.KeyID, key, vector.Sent); err == nil {
			t.Fatal("invalid health accepted", body)
		}
	}
	if err := VerifyHealthRequest(makeRequest(vector.Body, HealthPath, "epay-observer-v1"), []byte(vector.Body), vector.KeyID, key, vector.Sent); err == nil {
		t.Fatal("event domain accepted")
	}
	if _, err := VerifyRequest(makeRequest(vector.Body, Path, "epay-observer-health-v1"), []byte(vector.Body), vector.KeyID, key, vector.Sent); err == nil || err.Error() != "invalid_signature" {
		t.Fatal("health signature crossed event domain", err)
	}
	r.Header.Add("X-Observer-Signature", vector.Signature)
	if err := VerifyHealthRequest(r, []byte(vector.Body), vector.KeyID, key, vector.Sent); err == nil {
		t.Fatal("duplicate header accepted")
	}
}
