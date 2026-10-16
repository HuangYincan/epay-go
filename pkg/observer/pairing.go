package observer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const PairingPrefix = "/api/observer/v1/pairing/"
const PairingMaxBody = 4096

type PairingRequest struct {
	SchemaVersion int    `json:"schema_version"`
	EnrollmentID  string `json:"enrollment_id"`
	ResumeSecret  string `json:"resume_secret"`
}

func ValidIdentifier(value string) bool { return identifier.MatchString(value) }
func ValidUUID(value string) bool       { return uuid.MatchString(value) }
func ValidSecret(value string) bool {
	if len(value) != 43 {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
}
func ValidOrigin(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") { return false }
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 { return false }
	}
	return value == "https://"+u.Host && u.Host == strings.ToLower(u.Host)
}

// StrictObject rejects duplicates before decoding: encoding/json alone accepts them.
func StrictObject(body []byte, required []string) (map[string]json.RawMessage, error) {
	invalid := errors.New("invalid_payload")
	if len(body) == 0 || len(body) > PairingMaxBody || !utf8.Valid(body) || !validSurrogates(body) {
		return nil, invalid
	}
	allowed := make(map[string]bool)
	for _, field := range required {
		allowed[field] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			return nil, invalid
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || bytes.Equal(raw, []byte("null")) {
			return nil, invalid
		}
		fields[name] = raw
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	if _, err = decoder.Token(); err != io.EOF || len(fields) != len(required) {
		return nil, invalid
	}
	return fields, nil
}

func ParsePairing(body []byte, operation string) (PairingRequest, error) {
	fields := []string{"schema_version"}
	if operation == "redeem" || operation == "activate" {
		fields = append(fields, "enrollment_id", "resume_secret")
	} else if operation != "inspect" {
		return PairingRequest{}, errors.New("invalid_route")
	}
	raw, err := StrictObject(body, fields)
	if err != nil || string(raw["schema_version"]) != "1" {
		return PairingRequest{}, errors.New("invalid_payload")
	}
	var result PairingRequest
	if json.Unmarshal(body, &result) != nil || operation != "inspect" && (!ValidUUID(result.EnrollmentID) || !ValidSecret(result.ResumeSecret)) {
		return PairingRequest{}, errors.New("invalid_payload")
	}
	return result, nil
}

type PairingScope struct {
	SchemaVersion          int    `json:"schema_version"`
	GrantID                string `json:"grant_id"`
	Origin                 string `json:"origin"`
	Environment            string `json:"environment"`
	ReceiverID             string `json:"receiver_id"`
	Provider               string `json:"provider"`
	SourceClass            string `json:"source_class"`
	Mode                   string `json:"mode"`
	ExpectedBindingVersion int    `json:"expected_binding_version"`
	DeviceID               string `json:"device_id"`
	ExpiresAtMs            int64  `json:"expires_at_ms"`
}
type Enrollment struct {
	SchemaVersion  int    `json:"schema_version"`
	EnrollmentID   string `json:"enrollment_id"`
	Origin         string `json:"origin"`
	Environment    string `json:"environment"`
	ReceiverID     string `json:"receiver_id"`
	Provider       string `json:"provider"`
	SourceClass    string `json:"source_class"`
	DeviceID       string `json:"device_id"`
	BindingVersion int    `json:"binding_version"`
	KeyID          string `json:"key_id"`
	KeyBase64URL   string `json:"key_base64url"`
	NotBeforeMs    int64  `json:"not_before_ms"`
	ExpiresAtMs    int64  `json:"expires_at_ms"`
}

func (Enrollment) String() string     { return "Enrollment(redacted)" }
func (PairingRequest) String() string { return "PairingRequest(redacted)" }
