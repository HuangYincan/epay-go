package observer

import (
	"errors"
	"net/http"
)

const HealthPath = "/api/observer/v1/health"
const HealthMaxBody = 64

type HealthResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	ServerTimeMs  int64  `json:"server_time_ms"`
}

// Health is a separate signature domain. It never parses or fabricates an event.
func VerifyHealthRequest(r *http.Request, body []byte, keyID string, key []byte, now int64) error {
	if _, err := verifyEnvelope(r, body, keyID, key, now, HealthPath, "epay-observer-health-v1", HealthMaxBody); err != nil {
		return err
	}
	fields, err := StrictObject(body, []string{"schema_version"})
	if err != nil || string(fields["schema_version"]) != "1" {
		return errors.New("invalid_payload")
	}
	return nil
}
