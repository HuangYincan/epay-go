package observer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"unicode"
	"unicode/utf8"
)

type Event struct {
	SchemaVersion int64
	EventID string
	DeviceID string
	ReceiverID string
	BindingVersion int64
	Provider string
	AmountMinor int64
	Currency string
	ObservedAt int64
	ParserVersion string
	SourceClass string
	IdentityConfidence string
	ProviderTransactionID string // Empty means absent, never an invented event ID.
}

var parserVersion = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var requiredFields = []string{"schema_version", "event_id", "device_id", "receiver_id", "binding_version", "provider", "amount_minor", "currency", "observed_at_ms", "parser_version", "source_class", "identity_confidence"}

// ParseEvent is strict lexical validation, not proof of payment or source trust.
func ParseEvent(body []byte) (Event, error) {
	invalid := errors.New("invalid_payload")
	if len(body) == 0 || len(body) > MaxBody || !utf8.Valid(body) || !validSurrogates(body) {
		return Event{}, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Event{}, invalid
	}
	fields := make(map[string]json.RawMessage)
	allowed := make(map[string]bool)
	for _, field := range requiredFields {
		allowed[field] = true
	}
	allowed["provider_transaction_id"] = true
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || fields[name] != nil {
			return Event{}, invalid
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return Event{}, invalid
		}
		fields[name] = raw
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Event{}, invalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return Event{}, invalid
	}
	for _, name := range requiredFields {
		if fields[name] == nil {
			return Event{}, invalid
		}
	}
	good := true
	integer := func(name string, min, max int64) int64 {
		raw := string(fields[name])
		n, err := strconv.ParseInt(raw, 10, 64)
		if !decimalInteger.MatchString(raw) || err != nil || n < min || n > max {
			good = false
		}
		return n
	}
	text := func(name string) string {
		raw := fields[name]
		var value string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
			good = false
		}
		return value
	}
	event := Event{
		SchemaVersion: integer("schema_version", 1, 1), EventID: text("event_id"), DeviceID: text("device_id"),
		ReceiverID: text("receiver_id"), BindingVersion: integer("binding_version", 1, 2147483647),
		Provider: text("provider"), AmountMinor: integer("amount_minor", 1, 999999999999), Currency: text("currency"),
		ObservedAt: integer("observed_at_ms", 0, MaxTime), ParserVersion: text("parser_version"),
		SourceClass: text("source_class"), IdentityConfidence: text("identity_confidence"),
	}
	if fields["provider_transaction_id"] != nil {
		event.ProviderTransactionID = text("provider_transaction_id")
		n := utf8.RuneCountInString(event.ProviderTransactionID)
		if n < 1 || n > 128 || event.ProviderTransactionID == event.EventID {
			good = false
		}
		for _, c := range event.ProviderTransactionID {
			if unicode.IsControl(c) {
				good = false
			}
		}
	}
	if !good || !uuid.MatchString(event.EventID) || !identifier.MatchString(event.DeviceID) || !identifier.MatchString(event.ReceiverID) || !parserVersion.MatchString(event.ParserVersion) || event.Currency != "CNY" || (event.Provider != "wxpay" && event.Provider != "alipay") || (event.SourceClass != "synthetic" && event.SourceClass != "notification") || (event.IdentityConfidence != "distinct" && event.IdentityConfidence != "uncertain") {
		return Event{}, invalid
	}
	return event, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD; the wire contract
// instead rejects them. Valid scalar U+FFFD and valid surrogate pairs are allowed.
func validSurrogates(body []byte) bool {
	inString := false
	for i := 0; i < len(body); i++ {
		if body[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		unit, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil || (unit >= 0xdc00 && unit <= 0xdfff) {
			return false
		}
		i += 4
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
