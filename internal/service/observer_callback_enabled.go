//go:build observersynthetic

package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/safehttp"
)

var errSyntheticCallback = errors.New("synthetic callback configuration or scope rejected")

// localCallbackTransport is separate from safehttp and exists only in the
// explicit synthetic build. No caller can choose the dial destination.
func localCallbackTransport(raw string) (*http.Client, error) {
	configured := os.Getenv("OBSERVER_SYNTHETIC_CALLBACK_URL")
	if configured == "" || raw != configured || observerstore.SyntheticEnvironment() == "" {
		return nil, errSyntheticCallback
	}
	u, err := url.Parse(configured)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(configured, "#") || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.Path != "/api/user/epay/notify" || u.RawPath != "" {
		return nil, errSyntheticCallback
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return nil, errSyntheticCallback
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 || u.Port() != strconv.Itoa(port) {
		return nil, errSyntheticCallback
	}
	certPEM, err := os.ReadFile(os.Getenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE"))
	if err != nil {
		return nil, errSyntheticCallback
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		return nil, errSyntheticCallback
	}
	transport := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 5 * time.Second,
		DisableKeepAlives:   true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != u.Host {
				return nil, errSyntheticCallback
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), u.Port()))
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errSyntheticCallback }}, nil
}

func validateMerchantCallback(ctx context.Context, raw string, scope *observerstore.OrderScope) error {
	if raw != os.Getenv("OBSERVER_SYNTHETIC_CALLBACK_URL") || raw == "" {
		return safehttp.ValidateURL(ctx, raw)
	}
	if scope == nil || scope.Environment != observerstore.SyntheticEnvironment() {
		return errSyntheticCallback
	}
	_, err := localCallbackTransport(raw)
	return err
}

func syntheticCallbackClient(ctx context.Context, order *model.Order) (*http.Client, error) {
	if order.NotifyURL != os.Getenv("OBSERVER_SYNTHETIC_CALLBACK_URL") || order.NotifyURL == "" {
		return nil, nil
	}
	client, err := localCallbackTransport(order.NotifyURL)
	if err != nil {
		return nil, err
	}
	// Only an actual committed observer income may use the local transport.
	// Pairing/key rotation must not rewrite this historical settlement.
	var eligible bool
	err = database.Get().WithContext(ctx).Raw(`SELECT EXISTS (
        SELECT 1 FROM observer_amount_claims a
        JOIN observer_consumptions c ON c.order_id=a.order_id
        JOIN observer_events e ON e.id=c.event_pk AND e.environment=a.environment AND e.receiver_id=a.receiver_id AND e.provider=a.provider
        JOIN observer_environments env ON env.environment=a.environment
        JOIN observer_receivers r ON r.environment=a.environment AND r.receiver_id=a.receiver_id AND r.provider=a.provider
        JOIN observer_synthetic_merchants sm ON sm.environment=a.environment AND sm.merchant_id=a.merchant_id
        JOIN orders o ON o.id=a.order_id AND o.merchant_id=a.merchant_id
        JOIN merchants m ON m.id=o.merchant_id
        WHERE o.id=? AND o.trade_no=? AND o.notify_url=? AND o.status=1
          AND a.environment=? AND env.source_class='synthetic' AND env.enabled AND r.enabled AND sm.enabled
          AND e.source_class='synthetic' AND e.processing_status='credited' AND m.status=1 AND m.deleted_at IS NULL
    )`, order.ID, order.TradeNo, order.NotifyURL, observerstore.SyntheticEnvironment()).Scan(&eligible).Error
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, errSyntheticCallback
	}
	return client, nil
}
