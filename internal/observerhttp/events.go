package observerhttp

import (
	"net/http"
	"strconv"

	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/observer"
)

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
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
	if !exactRoute(r, observer.Path) {
		failure(w, 400, "invalid_route")
		return
	}
	body, ok := readBody(w, r, observer.MaxBody)
	if !ok {
		return
	}
	result := observerstore.NewReceiver(h.service).Receive(r.Context(), r, body, h.clock().UnixMilli())
	if result.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfterSeconds))
	}
	if result.Receipt != nil {
		write(w, result.Status, result.Receipt)
		return
	}
	failure(w, result.Status, result.Code)
}
