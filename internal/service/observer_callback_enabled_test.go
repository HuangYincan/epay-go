//go:build observersynthetic

package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/example/epay-go/internal/observerstore"
)

func callbackTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "callback-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "test")
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_URL", server.URL+"/api/user/epay/notify")
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", ca)
	return server
}

func TestSyntheticCallbackExactTLSOnly(t *testing.T) {
	var hits atomic.Int32
	server := callbackTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("redirect") != "" {
			http.Redirect(w, r, "https://127.0.0.1:12345/private", http.StatusFound)
			return
		}
		w.Write([]byte("success"))
	}))
	endpoint := server.URL + "/api/user/epay/notify"
	client, err := localCallbackTransport(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("no actual verified TLS request")
	}
	if _, err := client.Get(endpoint + "?redirect=1"); err == nil {
		t.Fatal("redirect followed")
	}
	for _, raw := range []string{
		strings.Replace(endpoint, "https:", "http:", 1), endpoint + "?x=1", endpoint + "?", endpoint + "#fragment", endpoint + "#", endpoint + "/", strings.Replace(endpoint, "/api/user/epay/notify", "/api/user/epay/%6eotify", 1),
		"https://localhost:12345/api/user/epay/notify", "https://127.0.0.1:443/api/user/epay/notify", "https://10.0.0.1:8444/api/user/epay/notify", "https://user@127.0.0.1:8444/api/user/epay/notify",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_URL", raw)
			if _, err := localCallbackTransport(raw); err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
	if _, err := localCallbackTransport(endpoint + "/different"); err == nil {
		t.Fatal("target mismatch")
	}
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := localCallbackTransport(endpoint); err == nil {
		t.Fatal("missing CA accepted")
	}
}

func TestSyntheticCallbackCreationScope(t *testing.T) {
	f := newClaimFixture(t)
	callbackTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("creation must not send callback") }))
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "claim-test")
	r := f.request("local-url", "wxpay", 0, 0)
	r.MerchantNotifyURL = os.Getenv("OBSERVER_SYNTHETIC_CALLBACK_URL")
	if _, err := f.service.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []*observerstore.OrderScope{nil, {Environment: "another-environment"}} {
		if err := validateMerchantCallback(context.Background(), r.MerchantNotifyURL, scope); err == nil {
			t.Fatal("scope bypass")
		}
	}
	// Retry of an existing ordinary order must validate the current CA too.
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	if _, err := f.service.Create(context.Background(), r); err == nil {
		t.Fatal("cached order bypassed callback validation")
	}
}

func TestSyntheticCallbackRejectsCertificateMismatchAndUncreditedOrder(t *testing.T) {
	var hits atomic.Int32
	server := callbackTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte("success")) }))
	endpoint := server.URL + "/api/user/epay/notify"
	// A trusted self-signed certificate with the wrong SAN must still fail.
	leaf := *server.Certificate()
	leaf.IPAddresses = nil
	leaf.DNSNames = []string{"wrong.example"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf.PublicKey = &key.PublicKey
	leaf.PublicKeyAlgorithm = x509.ECDSA
	leaf.SignatureAlgorithm = x509.ECDSAWithSHA256
	der, err := x509.CreateCertificate(rand.Reader, &leaf, &leaf, leaf.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("wrong SAN reached handler") }))
	wrong.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	wrong.StartTLS()
	defer wrong.Close()
	ca := filepath.Join(t.TempDir(), "wrong-san.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("untrusted root", func(t *testing.T) {
		t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", ca)
		client, err := localCallbackTransport(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Get(endpoint); err == nil {
			t.Fatal("untrusted root accepted")
		}
	})
	t.Run("trusted wrong SAN", func(t *testing.T) {
		raw := wrong.URL + "/api/user/epay/notify"
		t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_URL", raw)
		t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", ca)
		client, err := localCallbackTransport(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Get(raw); err == nil {
			t.Fatal("wrong IP SAN accepted")
		}
	})
	svc, order := notificationFixture(t)
	order.NotifyURL = endpoint
	if err := svc.orderRepo.Update(&order); err != nil {
		t.Fatal(err)
	}
	if err := svc.SendNotify(&order); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("ordinary paid order borrowed synthetic callback scope")
	}
}
