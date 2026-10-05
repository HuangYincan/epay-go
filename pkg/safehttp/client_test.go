package safehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublicAddresses(t *testing.T) {
	for _, v := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.1.1.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.1.1", "0.0.0.0", "fc00::1", "fe80::1", "192.0.2.1", "64:ff9b::a00:1"} {
		if IsPublicIP(netip.MustParseAddr(v)) {
			t.Fatalf("allowed %s", v)
		}
	}
	for _, v := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !IsPublicIP(netip.MustParseAddr(v)) {
			t.Fatalf("blocked %s", v)
		}
	}
}
func TestPrivateWebhookNeverConnects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("private target contacted") }))
	defer server.Close()
	if err := ValidateURL(context.Background(), server.URL); err == nil {
		t.Fatal("loopback validation succeeded")
	}
	if _, err := NewClient().Get(server.URL); err == nil {
		t.Fatal("loopback connection succeeded")
	}
	if _, err := NewClient().Get("http://localhost/internal"); err == nil {
		t.Fatal("DNS loopback connection succeeded")
	}
}
func TestWebhookSchemes(t *testing.T) {
	for _, v := range []string{"file:///etc/passwd", "ftp://8.8.8.8/", "http://user:password@8.8.8.8", "javascript:alert(1)"} {
		if _, err := ParseURL(v); err == nil {
			t.Fatalf("accepted %s", v)
		}
	}
}
