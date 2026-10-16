// Package observerhttp serves the startup-fixed observer trust domain over TLS.
package observerhttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
)

type Handler struct {
	service *observerstore.PairingService
	clock   func() time.Time
	slots   chan struct{}
	mu      sync.Mutex
	tokens  float64
	last    time.Time
	host    string
}

func NewHandler(service *observerstore.PairingService) *Handler {
	origin, _ := url.Parse(service.Origin())
	return &Handler{service: service, clock: time.Now, slots: make(chan struct{}, 16), tokens: 20, last: time.Now(), host: origin.Host}
}
func single(h http.Header, name string) (string, bool) {
	value := ""
	count := 0
	for key, values := range h {
		if strings.EqualFold(key, name) {
			count++
			if len(values) != 1 {
				return "", false
			}
			value = values[0]
		}
	}
	return value, count == 1
}
func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]any{"schema_version": 1, "code": code})
}
func serviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, observerstore.ErrInput):
		failure(w, 400, "invalid_payload")
	case errors.Is(err, observerstore.ErrGrant):
		failure(w, 401, "invalid_grant")
	case errors.Is(err, observerstore.ErrScope):
		failure(w, 403, "scope_denied")
	case errors.Is(err, observerstore.ErrEnrollment):
		failure(w, 409, "enrollment_conflict")
	case errors.Is(err, observerstore.ErrBinding):
		failure(w, 409, "binding_changed")
	case errors.Is(err, observerstore.ErrRate):
		w.Header().Set("Retry-After", "60")
		failure(w, 429, "rate_limited")
	default:
		failure(w, 503, "unavailable")
	}
}
func (h *Handler) take(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	delta := now.Sub(h.last).Seconds()
	if delta > 0 {
		h.tokens += delta
		if h.tokens > 20 {
			h.tokens = 20
		}
		h.last = now
	}
	if h.tokens < 1 {
		return false
	}
	h.tokens--
	return true
}

// Guard is also applied before administrator JWT parsing on the dedicated TLS
// listener. Forwarded headers never establish secure transport or the origin.
func (h *Handler) Guard(w http.ResponseWriter, r *http.Request) bool {
	if r.TLS == nil || r.Host != h.host {
		failure(w, 403, "scope_denied")
		return false
	}
	if !h.take(h.clock()) {
		w.Header().Set("Retry-After", "1")
		failure(w, 429, "rate_limited")
		return false
	}
	return true
}
func readBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, bool) {
	contentType, ok := single(r.Header, "Content-Type")
	kind, params, err := mime.ParseMediaType(contentType)
	if !ok || err != nil || kind != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		failure(w, 415, "unsupported_media_type")
		return nil, false
	}
	for name := range r.Header {
		if strings.EqualFold(name, "Content-Encoding") {
			failure(w, 400, "invalid_payload")
			return nil, false
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if len(body) > limit {
		failure(w, 413, "body_too_large")
		return nil, false
	}
	if err != nil {
		failure(w, 400, "invalid_payload")
		return nil, false
	}
	return body, true
}
func exactRoute(r *http.Request, path string) bool {
	return r.Method == http.MethodPost && r.URL.Path == path && r.URL.RawPath == "" && r.URL.RawQuery == "" && !r.URL.ForceQuery && r.URL.Fragment == "" && (r.RequestURI == "" || r.RequestURI == path)
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.Guard(w, r) {
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		failure(w, 429, "rate_limited")
		return
	}
	operation := strings.TrimPrefix(r.URL.Path, observer.PairingPrefix)
	if (operation != "inspect" && operation != "redeem" && operation != "activate") || !exactRoute(r, observer.PairingPrefix+operation) {
		failure(w, 400, "invalid_route")
		return
	}
	auth, ok := single(r.Header, "Authorization")
	token := strings.TrimPrefix(auth, "ObserverPairing ")
	if !ok || !strings.HasPrefix(auth, "ObserverPairing ") || !observer.ValidSecret(token) {
		failure(w, 401, "invalid_grant")
		return
	}
	body, ok := readBody(w, r, observer.PairingMaxBody)
	if !ok {
		return
	}
	req, err := observer.ParsePairing(body, operation)
	if err != nil {
		failure(w, 400, "invalid_payload")
		return
	}
	now := h.clock().UnixMilli()
	if operation == "inspect" {
		result, err := h.service.Inspect(r.Context(), token, now)
		if err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	if operation == "redeem" {
		result, err := h.service.Redeem(r.Context(), token, req, now)
		if err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, result)
		return
	}
	result, err := h.service.Activate(r.Context(), token, req, now)
	if err != nil {
		serviceError(w, err)
		return
	}
	write(w, 200, map[string]any{"schema_version": 1, "enrollment_id": result.EnrollmentID, "key_id": result.KeyID, "state": "active"})
}

// ServeAdmin is called only after the ordinary administrator JWT middleware.
// Actor is never accepted from a JSON body or header controlled by the device.
func (h *Handler) ServeAdmin(w http.ResponseWriter, r *http.Request, actor int64) {
	if actor <= 0 {
		failure(w, 403, "scope_denied")
		return
	}
	body, ok := readBody(w, r, observer.PairingMaxBody)
	if !ok {
		return
	}
	switch {
	case exactRoute(r, "/api/admin/observer/grants"):
		fields, err := observer.StrictObject(body, []string{"schema_version", "receiver_id", "mode"})
		var req struct {
			SchemaVersion int    `json:"schema_version"`
			ReceiverID    string `json:"receiver_id"`
			Mode          string `json:"mode"`
		}
		if err != nil || string(fields["schema_version"]) != "1" || json.Unmarshal(body, &req) != nil {
			failure(w, 400, "invalid_payload")
			return
		}
		scope, token, err := h.service.CreateGrant(r.Context(), req.ReceiverID, req.Mode, actor, h.clock().UnixMilli())
		if err != nil {
			serviceError(w, err)
			return
		}
		write(w, 201, struct {
			observer.PairingScope
			Token string `json:"token"`
		}{scope, token})
	case exactRoute(r, "/api/admin/observer/grants/revoke"):
		fields, err := observer.StrictObject(body, []string{"schema_version", "grant_id"})
		var req struct {
			GrantID string `json:"grant_id"`
		}
		if err != nil || string(fields["schema_version"]) != "1" || json.Unmarshal(body, &req) != nil {
			failure(w, 400, "invalid_payload")
			return
		}
		if err := h.service.RevokeGrant(r.Context(), req.GrantID); err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, map[string]any{"schema_version": 1, "state": "revoked"})
	case exactRoute(r, "/api/admin/observer/bindings/revoke"):
		fields, err := observer.StrictObject(body, []string{"schema_version", "receiver_id", "binding_version"})
		var req struct {
			ReceiverID string `json:"receiver_id"`
			Version    int    `json:"binding_version"`
		}
		if err != nil || string(fields["schema_version"]) != "1" || json.Unmarshal(body, &req) != nil {
			failure(w, 400, "invalid_payload")
			return
		}
		if err := h.service.RevokeBinding(r.Context(), req.ReceiverID, req.Version, h.clock().UnixMilli()); err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, map[string]any{"schema_version": 1, "state": "revoked"})
	default:
		failure(w, 400, "invalid_route")
	}
}
