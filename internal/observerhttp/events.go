package observerhttp

import (
	"net/http"
	"strconv"

	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
)

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) { h.deviceRequest(w, r, false) }
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) { h.deviceRequest(w, r, true) }

func (h *Handler) deviceRequest(w http.ResponseWriter, r *http.Request, health bool) {
	path, size := observer.Path, observer.MaxBody
	if health {
		path, size = observer.HealthPath, observer.HealthMaxBody
	}

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
	if !exactRoute(r, path) {
		failure(w, 400, "invalid_route")
		return
	}
	body, ok := readBody(w, r, size)
	if !ok {
		return
	}
	receiver := observerstore.NewReceiver(h.service)
	var result observerstore.ReceiveResult
	if health {
		result = receiver.CheckHealth(r.Context(), r, body, h.clock().UnixMilli())
	} else {
		result = receiver.Receive(r.Context(), r, body, h.clock().UnixMilli())
	}
	if result.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfterSeconds))
	}
	if result.Health != nil {
		write(w, result.Status, result.Health)
		return
	}
	if result.Receipt != nil {
		write(w, result.Status, result.Receipt)
		return
	}
	failure(w, result.Status, result.Code)
}
