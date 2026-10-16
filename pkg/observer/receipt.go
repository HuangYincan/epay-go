package observer

import "net/http"

type Receipt struct {
	SchemaVersion    int    `json:"schema_version"`
	EventID          string `json:"event_id"`
	ReceiptStatus    string `json:"receipt_status"`
	ProcessingStatus string `json:"processing_status"`
	ReceivedAtMs     int64  `json:"received_at_ms"`
}

// RequestKeyID selects a credential; it does not authenticate the request.
func RequestKeyID(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	key, ok := singleHeader(r.Header, "X-Observer-Key-Id")
	return key, ok && identifier.MatchString(key)
}
