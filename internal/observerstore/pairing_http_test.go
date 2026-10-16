package observerstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
)

func TestPairingActualTLSAndStrictHTTP(t *testing.T) {
	db, _, actor := pairingDB(t)
	server := httptest.NewUnstartedServer(nil)
	cipher, err := observerstore.NewKeyCipher("test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "test", "synthetic", "https://"+server.Listener.Addr().String(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = observerhttp.Router(svc)
	server.StartTLS()
	defer server.Close()
	now := time.Now().UnixMilli()
	token := grant(t, svc, actor, "replace", now)
	client := server.Client()
	call := func(path, body string, mutate func(*http.Request)) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "ObserverPairing "+token)
		if mutate != nil {
			mutate(r)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Cache-Control") != "no-store" && response.StatusCode != 401 {
			t.Fatal("secret response may be cached")
		}
		return response.StatusCode, data
	}
	code, data := call(observer.PairingPrefix+"inspect", `{"schema_version":1}`, nil)
	if code != 200 {
		t.Fatalf("inspect status %d: %s", code, data)
	}
	var scope observer.PairingScope
	if err := json.Unmarshal(data, &scope); err != nil || scope.Origin != server.URL {
		t.Fatal("origin not fixed", err)
	}
	body, _ := json.Marshal(request())
	code, data = call(observer.PairingPrefix+"redeem", string(body), nil)
	var enrollment observer.Enrollment
	if code != 200 || json.Unmarshal(data, &enrollment) != nil || !observer.ValidSecret(enrollment.KeyBase64URL) {
		t.Fatalf("redeem status %d", code)
	}
	code, data = call(observer.PairingPrefix+"activate", string(body), nil)
	if code != 200 || bytes.Contains(data, []byte("key_base64url")) {
		t.Fatal("activate leaked secret", code)
	}
	for _, tc := range []struct {
		name, path, body string
		want             int
		mutate           func(*http.Request)
	}{
		{"query", observer.PairingPrefix + "inspect?x=y", `{"schema_version":1}`, 400, nil},
		{"duplicate JSON", observer.PairingPrefix + "inspect", `{"schema_version":1,"schema_version":1}`, 400, nil},
		{"media type", observer.PairingPrefix + "inspect", `{"schema_version":1}`, 415, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"duplicate authorization", observer.PairingPrefix + "inspect", `{"schema_version":1}`, 401, func(r *http.Request) { r.Header.Add("Authorization", "ObserverPairing "+token) }},
		{"oversized", observer.PairingPrefix + "inspect", strings.Repeat(" ", 4097), 413, nil},
		{"origin mismatch", observer.PairingPrefix + "inspect", `{"schema_version":1}`, 403, func(r *http.Request) { r.Host = "other.example" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, data := call(tc.path, tc.body, tc.mutate)
			if code != tc.want {
				t.Fatalf("status=%d want=%d", code, tc.want)
			}
			if bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(request().ResumeSecret)) {
				t.Fatal("error echoed secret")
			}
		})
	}
	// The ordinary client's trust store does not trust this test CA.
	untrusted := &http.Client{Timeout: 3 * time.Second}
	if response, err := untrusted.Get(server.URL + observer.PairingPrefix + "inspect"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted TLS accepted")
	}
	// Forwarded metadata cannot turn a cleartext handler call into HTTPS.
	r := httptest.NewRequest("POST", server.URL+observer.PairingPrefix+"inspect", strings.NewReader(`{"schema_version":1}`))
	r.TLS = nil
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	observerhttp.NewPairingHandler(svc).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("forwarded TLS spoof accepted")
	}
	code, _ = call("/api/admin/observer/grants", `{"schema_version":1,"receiver_id":"fixture-receiver-wx","mode":"replace"}`, func(r *http.Request) { r.Header.Del("Authorization") })
	if code != 401 {
		t.Fatal("admin route not authenticated", code)
	}
	// Revoke after activation; the actual HTTPS recovery path must now fail.
	if err := svc.RevokeBinding(context.Background(), enrollment.ReceiverID, enrollment.BindingVersion, now+1); err != nil {
		t.Fatal(err)
	}
	code, _ = call(observer.PairingPrefix+"redeem", string(body), nil)
	if code != 403 {
		t.Fatal("revoked credential returned via HTTPS", code)
	}
}
