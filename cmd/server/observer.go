package main

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/observerhttp"
	"github.com/example/epay-go/internal/observerstore"
)

// Observer requires a separate TLS socket and a complete startup-fixed trust
// domain. The ordinary HTTP router cannot be promoted by forwarded headers.
func observerServer() (*http.Server, net.Listener, error) {
	environment := os.Getenv("OBSERVER_ENVIRONMENT")
	fields := []string{"OBSERVER_SOURCE_CLASS", "OBSERVER_ORIGIN", "OBSERVER_KEK_ID", "OBSERVER_KEK_FILE", "OBSERVER_TLS_CERT_FILE", "OBSERVER_TLS_KEY_FILE", "OBSERVER_TLS_ADDRESS"}
	configured := environment != ""
	for _, name := range fields {
		configured = configured || os.Getenv(name) != ""
	}
	if !configured {
		return nil, nil, nil
	}
	invalid := errors.New("observer configuration incomplete or invalid")
	if environment == "" {
		return nil, nil, invalid
	}
	for _, name := range fields {
		if os.Getenv(name) == "" {
			return nil, nil, invalid
		}
	}
	file, err := os.Open(os.Getenv("OBSERVER_KEK_FILE"))
	if err != nil {
		return nil, nil, invalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128 {
		return nil, nil, invalid
	}
	raw := make([]byte, info.Size())
	if _, err := file.ReadAt(raw, 0); err != nil {
		return nil, nil, invalid
	}
	defer clear(raw)
	material, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, nil, invalid
	}
	cipher, err := observerstore.NewKeyCipher(os.Getenv("OBSERVER_KEK_ID"), material)
	if err != nil {
		return nil, nil, invalid
	}
	service, err := observerstore.NewPairingService(database.Get(), environment, os.Getenv("OBSERVER_SOURCE_CLASS"), os.Getenv("OBSERVER_ORIGIN"), cipher)
	if err != nil {
		return nil, nil, invalid
	}
	certificate, err := tls.LoadX509KeyPair(os.Getenv("OBSERVER_TLS_CERT_FILE"), os.Getenv("OBSERVER_TLS_KEY_FILE"))
	if err != nil {
		return nil, nil, invalid
	}
	listener, err := net.Listen("tcp", os.Getenv("OBSERVER_TLS_ADDRESS"))
	if err != nil {
		return nil, nil, errors.New("observer TLS listener unavailable")
	}
	server := &http.Server{Handler: observerhttp.Router(service), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
	return server, tls.NewListener(listener, server.TLSConfig), nil
}
