//go:build observeravd

package observerstore_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
)

// This bridge exists only in an explicitly tagged test binary, uses testDB's
// disposable schema, and is never compiled into cmd/server. It returns no keys
// in evidence files. Pairing grants are private, short-lived bootstrap inputs.
func TestPairingAVDBridge(t *testing.T) {
	directory := os.Getenv("OBSERVER_AVD_DIR")
	if directory == "" {
		t.Skip("explicit private AVD directory required")
	}
	mode := os.Getenv("OBSERVER_AVD_MODE")
	if mode != "" && mode != "upload" && mode != "background" {
		t.Fatal("unknown AVD mode")
	}
	marker, reportName := "device-paired", "server-result.json"
	if mode == "upload" {
		marker, reportName = "device-uploaded", "upload-server-result.json"
	}
	if mode == "background" {
		marker, reportName = "device-background", "background-server-result.json"
	}
	db, _, actor := pairingDB(t)
	cipher, err := observerstore.NewKeyCipher("avd-test-kek", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := observerstore.NewPairingService(db, "test", "synthetic", "https://localhost:18443", cipher)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server.key"))
	if err != nil {
		t.Fatal("test TLS certificate unavailable")
	}
	listener, err := net.Listen("tcp", ":8443")
	if err != nil {
		t.Fatal(err)
	}
	router := observerhttp.Router(svc)
	var redirected atomic.Int64
	var droppedAck atomic.Bool
	// Negative transport fixtures only. Real grants always reach the real router.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-redirect-sink" {
			redirected.Add(1)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path == observer.PairingPrefix+"inspect" {
			switch r.Header.Get("Authorization") {
			case "ObserverPairing " + base64.RawURLEncoding.EncodeToString(make([]byte, 32)):
				http.Redirect(w, r, "/test-redirect-sink", http.StatusTemporaryRedirect)
				return
			case "ObserverPairing " + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)):
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(observer.PairingScope{SchemaVersion: 1, GrantID: "negative-origin", Origin: "https://unexpected.example", Environment: "test", ReceiverID: "fixture-receiver-wx", Provider: "wxpay", SourceClass: "synthetic", Mode: "replace", ExpiresAtMs: time.Now().UnixMilli() + 300000})
				return
			}
		}
		if mode != "" && r.URL.Path == observer.Path && !droppedAck.Load() {
			// Commit through the real receiver, then deliberately lose only the ACK
			// body. A valid 202 without a receipt must not finish the Android lease.
			recorded := httptest.NewRecorder()
			router.ServeHTTP(recorded, r)
			for name, values := range recorded.Header() {
				w.Header()[name] = values
			}
			w.WriteHeader(recorded.Code)
			if recorded.Code == 202 && droppedAck.CompareAndSwap(false, true) {
				return
			}
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		router.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}))
	}()
	defer server.Close()
	issue := func() {
		grants := []struct {
			Scope observer.PairingScope `json:"scope"`
			Token string                `json:"token"`
		}{}
		for _, receiver := range []string{"fixture-receiver-wx", "fixture-receiver-ali"} {
			scope, token, err := svc.CreateGrant(context.Background(), receiver, "replace", actor, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			grants = append(grants, struct {
				Scope observer.PairingScope `json:"scope"`
				Token string                `json:"token"`
			}{scope, token})
		}
		data, err := json.Marshal(grants)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "avd-grants.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	issue()
	t.Log("AVD TLS pairing bridge ready; private grants written; waiting for device evidence")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("TLS server ended: %v", err)
		case <-timeout.C:
			t.Fatal("AVD evidence timeout")
		case <-ticker.C:
			if mode == "background" {
				// Host fault orchestration observes only durable counts, without
				// starting instrumentation (which itself starts the Android app).
				progress, _ := json.Marshal(map[string]int64{"events": count(t, db, "observer_events"), "requests": count(t, db, "observer_requests")})
				path := filepath.Join(directory, "background-progress.json")
				if err := os.WriteFile(path+".tmp", progress, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".tmp", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(directory, "refresh-grants")); err == nil {
				if err := os.Remove(filepath.Join(directory, "refresh-grants")); err != nil {
					t.Fatal(err)
				}
				issue()
			}
			if _, err := os.Stat(filepath.Join(directory, marker)); err != nil {
				continue
			}
			var rows []struct {
				Provider string
				Count    int64
			}
			if err := db.Raw(`SELECT provider,count(*) FROM observer_credentials WHERE state='active' GROUP BY provider ORDER BY provider`).Scan(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0].Provider != "alipay" || rows[1].Provider != "wxpay" || rows[0].Count != 1 || rows[1].Count != 1 {
				t.Fatal("both Android activations were not committed")
			}
			if redirected.Load() != 0 {
				t.Fatal("Android followed a credential-bearing redirect")
			}
			report := map[string]any{"layer": "emulator synthetic pairing over HTTPS", "active_credentials": 2, "providers": []string{"wxpay", "alipay"}, "events": count(t, db, "observer_events"), "payment_verified": false, "redirect_requests": redirected.Load()}
			if mode != "" {
				wantEvents, wantRequests := int64(2), int64(3)
				if mode == "background" {
					wantEvents, wantRequests = 8, 9
				}
				if count(t, db, "observer_events") != wantEvents || count(t, db, "observer_requests") != wantRequests || !droppedAck.Load() {
					t.Fatal("real notification upload or ACK recovery incomplete")
				}
				var providers int64
				if err := db.Raw(`SELECT count(DISTINCT provider) FROM observer_events WHERE processing_status='pending' AND provider_transaction_id IS NULL`).Scan(&providers).Error; err != nil {
					t.Fatal(err)
				}
				if providers != 2 {
					t.Fatal("provider/evidence semantics changed")
				}
				report["layer"] = "emulator synthetic notifications over signed HTTPS"
				report["requests"] = wantRequests
				report["ack_body_dropped"] = true
				report["event_conflicts"] = count(t, db, "observer_event_conflicts")
			}
			data, _ := json.MarshalIndent(report, "", "  ")
			if err := os.WriteFile(filepath.Join(directory, reportName), data, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
