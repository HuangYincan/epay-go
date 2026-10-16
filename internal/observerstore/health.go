package observerstore

import (
	"context"
	"github.com/example/epay-go/pkg/observer"
	"gorm.io/gorm"
	"net/http"
)

// CheckHealth commits only device rate-budget changes. Event and payment tables
// are never touched, and repeated envelopes still recheck current authorization.
func (receiver *Receiver) CheckHealth(ctx context.Context, r *http.Request, body []byte, now int64) ReceiveResult {
	return receiver.withCredential(ctx, r, now, func(tx *gorm.DB, credential credentialRow, key []byte) (ReceiveResult, error) {
		err := observer.VerifyHealthRequest(r, body, credential.KeyID, key, now)
		clear(key)
		if err != nil {
			return verificationFailure(err.Error()), nil
		}
		retry, err := chargeDevice(tx, credential.Environment, credential.DeviceID, now)
		if err != nil {
			return ReceiveResult{}, err
		}
		if retry > 0 {
			return ReceiveResult{Status: 429, Code: "rate_limited", RetryAfterSeconds: retry}, nil
		}
		return ReceiveResult{Status: 200, Health: &observer.HealthResponse{SchemaVersion: 1, Status: "authorized", ServerTimeMs: now}}, nil
	})
}
