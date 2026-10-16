// Package observer validates observer-events-v1-rc1 bytes. Verification is not
// authorization, durable acceptance, transaction evidence, or payment approval.
package observer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const Path = "/api/observer/v1/events"
const MaxBody = 16384
const MaxTime int64 = 9007199254740991

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var decimalInteger = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
var lowerSignature = regexp.MustCompile(`^[0-9a-f]{64}$`)

// VerifiedRequest is not an ACK. The caller must authorize scope/revocation,
// persist replay and event records atomically, and only then send a receipt.
type VerifiedRequest struct {
	Event      Event
	RequestID  string
	SentAt     int64
	BodySHA256 string
	body       []byte
}

// BodyBytes returns a defensive copy of the bytes whose signature was verified.
func (v *VerifiedRequest) BodyBytes() []byte { return append([]byte(nil), v.body...) }
func (v *VerifiedRequest) String() string    { return "VerifiedRequest(redacted)" }

// VerifyRequest verifies exact bytes using an independently resolved device key.
// The caller must bound the actual HTTP body before invoking this pure function.
// It deliberately does not register routes, read databases, or send responses.
func VerifyRequest(r *http.Request, body []byte, keyID string, key []byte, receivedAt int64) (*VerifiedRequest, error) {
	envelope, err := verifyEnvelope(r, body, keyID, key, receivedAt, Path, "epay-observer-v1", MaxBody)
	if err != nil {
		return nil, err
	}
	event, err := ParseEvent(envelope.body)
	if err != nil {
		return nil, err
	}
	return &VerifiedRequest{Event: event, RequestID: envelope.requestID, SentAt: envelope.sentAt, BodySHA256: envelope.hash, body: envelope.body}, nil
}

type verifiedEnvelope struct {
	requestID string
	sentAt    int64
	hash      string
	body      []byte
}

func verifyEnvelope(r *http.Request, body []byte, keyID string, key []byte, receivedAt int64, path, domain string, maxBody int) (*verifiedEnvelope, error) {
	if r == nil || r.URL == nil || r.Method != "POST" || r.URL.Path != path || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || (r.RequestURI != "" && r.RequestURI != path) {
		return nil, errors.New("invalid_route")
	}
	if len(body) > maxBody {
		return nil, errors.New("body_too_large")
	}
	if len(body) == 0 {
		return nil, errors.New("invalid_payload")
	}
	contentType, ok := singleHeader(r.Header, "Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if !ok || err != nil || mediaType != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		return nil, errors.New("unsupported_media_type")
	}
	for name := range r.Header {
		if strings.EqualFold(name, "Content-Encoding") {
			return nil, errors.New("invalid_payload")
		}
	}
	version, ok := singleHeader(r.Header, "X-Observer-Version")
	if !ok || version != "1" {
		return nil, errors.New("unsupported_version")
	}
	providedKey, ok := singleHeader(r.Header, "X-Observer-Key-Id")
	if !ok || !identifier.MatchString(providedKey) || !identifier.MatchString(keyID) || providedKey != keyID || len(key) != 32 {
		return nil, errors.New("unknown_key")
	}
	sent, ok := singleHeader(r.Header, "X-Observer-Sent-At")
	sentAt, err := strconv.ParseInt(sent, 10, 64)
	if !ok || !decimalInteger.MatchString(sent) || err != nil || sentAt < 0 || sentAt > MaxTime || receivedAt < 0 || receivedAt > MaxTime || sentAt-receivedAt > 300000 || receivedAt-sentAt > 300000 {
		return nil, errors.New("clock_skew")
	}
	requestID, ok := singleHeader(r.Header, "X-Observer-Request-Id")
	if !ok || !uuid.MatchString(requestID) {
		return nil, errors.New("invalid_request")
	}
	signature, ok := singleHeader(r.Header, "X-Observer-Signature")
	if !ok || !lowerSignature.MatchString(signature) {
		return nil, errors.New("invalid_signature")
	}
	frozen := append([]byte(nil), body...)
	digest := sha256.Sum256(frozen)
	hash := hex.EncodeToString(digest[:])
	input := strings.Join([]string{domain, "POST", path, keyID, sent, requestID, hash}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(input))
	provided, _ := hex.DecodeString(signature)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return nil, errors.New("invalid_signature")
	}
	return &verifiedEnvelope{requestID: requestID, sentAt: sentAt, hash: hash, body: frozen}, nil
}

// Iterate names as well as values: callers may construct noncanonical Go Header
// maps, whereas HTTP proxies may combine duplicates into a comma-separated value.
func singleHeader(headers http.Header, wanted string) (string, bool) {
	value := ""
	count := 0
	for name, values := range headers {
		if strings.EqualFold(name, wanted) {
			count++
			if len(values) != 1 {
				return "", false
			}
			value = values[0]
		}
	}
	return value, count == 1
}
