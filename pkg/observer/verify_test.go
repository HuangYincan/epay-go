package observer

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

type vector struct {
	ID        string `json:"id"`
	KeyHex    string `json:"key_hex"`
	KeyID     string `json:"key_id"`
	SentAt    string `json:"sent_at_ms"`
	RequestID string `json:"request_id"`
	Body      string `json:"body_base64"`
	Hash      string `json:"body_sha256"`
	Input     string `json:"signing_input_base64"`
	Signature string `json:"signature_hex"`
}

func goldens(t *testing.T) []vector {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []vector `json:"vectors"`
	}
	if err = json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Vectors) != 3 {
		t.Fatalf("expected three golden vectors, got %d", len(file.Vectors))
	}
	return file.Vectors
}

func request(v vector) *http.Request {
	return &http.Request{Method: "POST", URL: &url.URL{Path: Path}, RequestURI: Path, Header: http.Header{
		"Content-Type": {"application/json; charset=utf-8"}, "X-Observer-Version": {"1"},
		"X-Observer-Key-Id": {v.KeyID}, "X-Observer-Sent-At": {v.SentAt},
		"X-Observer-Request-Id": {v.RequestID}, "X-Observer-Signature": {v.Signature},
	}}
}

func TestCommittedKotlinPythonJavaGoldens(t *testing.T) {
	for _, v := range goldens(t) {
		t.Run(v.ID, func(t *testing.T) {
			body, _ := base64.StdEncoding.DecodeString(v.Body)
			key, _ := hex.DecodeString(v.KeyHex)
			now, _ := strconv.ParseInt(v.SentAt, 10, 64)
			verified, err := VerifyRequest(request(v), body, v.KeyID, key, now)
			if err != nil {
				t.Fatal(err)
			}
			if verified.BodySHA256 != v.Hash || verified.RequestID != v.RequestID || !bytes.Equal(verified.BodyBytes(), body) {
				t.Fatal("verified values differ from golden")
			}
			digest := sha256.Sum256(body)
			input := strings.Join([]string{"epay-observer-v1", "POST", Path, v.KeyID, v.SentAt, v.RequestID, hex.EncodeToString(digest[:])}, "\n")
			if base64.StdEncoding.EncodeToString([]byte(input)) != v.Input {
				t.Fatal("canonical bytes differ")
			}
			mac := hmac.New(sha256.New, key)
			_, _ = mac.Write([]byte(input))
			if hex.EncodeToString(mac.Sum(nil)) != v.Signature {
				t.Fatal("signature differs")
			}
			if verified.String() != "VerifiedRequest(redacted)" {
				t.Fatal("diagnostics not redacted")
			}
			body[0] = 'x'
			verified.BodyBytes()[0] = 'y'
			if verified.BodyBytes()[0] != '{' {
				t.Fatal("body aliases caller memory")
			}
		})
	}
}

func TestEnvelopeMutationAndTimeBoundaries(t *testing.T) {
	v := goldens(t)[0]
	body, _ := base64.StdEncoding.DecodeString(v.Body)
	key, _ := hex.DecodeString(v.KeyHex)
	now, _ := strconv.ParseInt(v.SentAt, 10, 64)
	for _, delta := range []int64{-300000, 300000} {
		if _, err := VerifyRequest(request(v), body, v.KeyID, key, now+delta); err != nil {
			t.Fatal(err)
		}
	}
	for _, delta := range []int64{-300001, 300001} {
		if _, err := VerifyRequest(request(v), body, v.KeyID, key, now+delta); err == nil {
			t.Fatal("expired envelope accepted")
		}
	}
	mutations := map[string]func(*http.Request){
		"method":            func(r *http.Request) { r.Method = "GET" },
		"trailing-slash":    func(r *http.Request) { r.URL.Path += "/" },
		"query":             func(r *http.Request) { r.URL.RawQuery = "x=1" },
		"empty-query":       func(r *http.Request) { r.URL.ForceQuery = true },
		"encoded-path":      func(r *http.Request) { r.URL.RawPath = "/api/observer/v1/%65vents" },
		"uri":               func(r *http.Request) { r.RequestURI += "?x=1" },
		"version":           func(r *http.Request) { r.Header.Set("X-Observer-Version", "2") },
		"duplicate-version": func(r *http.Request) { r.Header.Add("X-Observer-Version", "1") },
		"duplicate-case":    func(r *http.Request) { r.Header["x-observer-version"] = []string{"1"} },
		"combined-header":   func(r *http.Request) { r.Header.Set("X-Observer-Version", "1,1") },
		"content-type":      func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"charset":           func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=latin1") },
		"unknown-parameter": func(r *http.Request) { r.Header.Set("Content-Type", "application/json; boundary=x") },
		"compression":       func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		"key-id":            func(r *http.Request) { r.Header.Set("X-Observer-Key-Id", "other") },
		"key-whitespace":    func(r *http.Request) { r.Header.Set("X-Observer-Key-Id", " "+v.KeyID) },
		"time-leading-zero": func(r *http.Request) { r.Header.Set("X-Observer-Sent-At", "0"+v.SentAt) },
		"time-float":        func(r *http.Request) { r.Header.Set("X-Observer-Sent-At", v.SentAt+".0") },
		"request-id":        func(r *http.Request) { r.Header.Set("X-Observer-Request-Id", "not-an-id") },
		"signature-case":    func(r *http.Request) { r.Header.Set("X-Observer-Signature", strings.ToUpper(v.Signature)) },
		"signature":         func(r *http.Request) { r.Header.Set("X-Observer-Signature", strings.Repeat("0", 64)) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := request(v)
			mutate(r)
			if _, err := VerifyRequest(r, body, v.KeyID, key, now); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	for _, badBody := range [][]byte{nil, append(append([]byte(nil), body...), ' '), bytes.Repeat([]byte{'x'}, MaxBody+1)} {
		if _, err := VerifyRequest(request(v), badBody, v.KeyID, key, now); err == nil {
			t.Fatal("body mutation accepted")
		}
	}
	if _, err := VerifyRequest(request(v), body, v.KeyID, key[:31], now); err == nil {
		t.Fatal("non-device key accepted")
	}
	wrong := bytes.Repeat([]byte{255}, 32)
	if _, err := VerifyRequest(request(v), body, v.KeyID, wrong, now); err == nil {
		t.Fatal("wrong key accepted")
	}
}
