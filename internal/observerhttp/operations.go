package observerhttp

import (
	"context"
	"encoding/json"
	"github.com/example/epay-go/pkg/observer"
	"net/http"
	"time"
)

// Installed behind the same dedicated TLS/origin and administrator JWT guards
// as grant issuance. Neither actor nor environment is accepted from the body.
func (h *Handler) ServeOperations(w http.ResponseWriter, r *http.Request, actor int64) {
	if actor <= 0 {
		failure(w, 403, "scope_denied")
		return
	}
	body, ok := readBody(w, r, observer.PairingMaxBody)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch {
	case exactRoute(r, "/api/admin/observer/confirmation"):
		fields, err := observer.StrictObject(body, []string{"schema_version", "receiver_id", "paused", "reason_code"})
		var req struct {
			SchemaVersion int    `json:"schema_version"`
			ReceiverID    string `json:"receiver_id"`
			Paused        bool   `json:"paused"`
			ReasonCode    string `json:"reason_code"`
		}
		if err != nil || string(fields["schema_version"]) != "1" || (string(fields["paused"]) != "true" && string(fields["paused"]) != "false") || json.Unmarshal(body, &req) != nil {
			failure(w, 400, "invalid_payload")
			return
		}
		result, err := h.service.SetConfirmationPaused(ctx, req.ReceiverID, req.Paused, req.ReasonCode, actor, h.clock().UnixMilli())
		if err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, result)
	case exactRoute(r, "/api/admin/observer/events/query"):
		fields, err := observer.StrictObject(body, []string{"schema_version", "receiver_id", "before_event_pk", "limit"})
		var req struct {
			SchemaVersion int    `json:"schema_version"`
			ReceiverID    string `json:"receiver_id"`
			Before        int64  `json:"before_event_pk"`
			Limit         int    `json:"limit"`
		}
		if err != nil || string(fields["schema_version"]) != "1" || string(fields["before_event_pk"]) == "null" || json.Unmarshal(body, &req) != nil {
			failure(w, 400, "invalid_payload")
			return
		}
		result, err := h.service.QueryEventTrace(ctx, req.ReceiverID, req.Before, req.Limit, actor)
		if err != nil {
			serviceError(w, err)
			return
		}
		write(w, 200, result)
	default:
		failure(w, 400, "invalid_route")
	}
}
