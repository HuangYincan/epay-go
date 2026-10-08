// Package sign implements the MD5 signing rules used by the EPay protocol.
package sign

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// canonicalString returns the EPay canonical payload. EPay excludes sign and
// sign_type, ignores empty values, sorts keys, and appends the shared key.
// Duplicate values are rejected because url.Values.Get would otherwise make
// the signed value differ from the value consumed by another parser.
func canonicalString(params url.Values, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("empty signing key")
	}
	keys := make([]string, 0, len(params))
	for k, values := range params {
		if len(values) > 1 {
			return "", errors.New("duplicate signing parameter")
		}
		if k == "sign" || k == "sign_type" {
			continue
		}
		if len(values) == 1 && values[0] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k][0])
	}
	b.WriteString(key)
	return b.String(), nil
}

// VerifyMD5Sign verifies an EPay MD5 signature. Comparison is constant time
// after strict hexadecimal decoding so malformed signatures are rejected early.
func VerifyMD5Sign(params url.Values, key, signature string) bool {
	if len(signature) != md5.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || len(provided) != md5.Size {
		return false
	}
	payload, err := canonicalString(params, key)
	if err != nil {
		return false
	}
	expected := md5.Sum([]byte(payload))
	return subtle.ConstantTimeCompare(expected[:], provided) == 1
}

// GenerateMD5Sign generates an EPay MD5 signature. It returns an empty string
// for an invalid key or ambiguous parameter set.
func GenerateMD5Sign(params url.Values, key string) string {
	payload, err := canonicalString(params, key)
	if err != nil {
		return ""
	}
	hash := md5.Sum([]byte(payload))
	return hex.EncodeToString(hash[:])
}
