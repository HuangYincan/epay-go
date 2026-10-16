package observer

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestStrictEventJSONAndIntegerGrammar(t *testing.T) {
	v := goldens(t)[0]
	bytes, _ := base64.StdEncoding.DecodeString(v.Body)
	base := string(bytes)
	cases := map[string]string{
		"duplicate": strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"escaped-duplicate": strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_versio\u006e":1`, 1),
		"unknown": strings.TrimSuffix(base, "}")+`,"order_id":"not-allowed"}`,
		"trailing": base+"{}",
		"bom": "\xef\xbb\xbf"+base,
		"invalid-utf8": strings.Replace(base, "fixture-key", "\xff", 1)+"\xff",
		"not-object": "["+base+"]",
		"missing": strings.Replace(base, `"currency":"CNY",`, "", 1),
		"null": strings.Replace(base, `"currency":"CNY"`, `"currency":null`, 1),
		"wrong-provider": strings.Replace(base, `"wxpay"`, `"wechat"`, 1),
		"bad-id": strings.Replace(base, `"device_id":"fixture-device-a"`, `"device_id":"设备"`, 1),
		"bad-parser": strings.Replace(base, `"fixture-wx-v1"`, `"rule/unsafe"`, 1),
		"bad-source": strings.Replace(base, `"synthetic"`, `"trusted"`, 1),
		"bad-confidence": strings.Replace(base, `"distinct"`, `"certain"`, 1),
	}
	for _, number := range []string{"0", "-0", "-1", "1.0", "1e3", `"1001"`, "1000000000000", "9223372036854775808", "null", "true", "{}", "[]"} {
		cases["amount-"+number] = strings.Replace(base, `"amount_minor":1001`, `"amount_minor":`+number, 1)
	}
	for _, number := range []string{"0", "2147483648", "1.0"} {
		cases["binding-"+number] = strings.Replace(base, `"binding_version":1`, `"binding_version":`+number, 1)
	}
	for _, number := range []string{"-1", "9007199254740992", "1e3"} {
		cases["time-"+number] = strings.Replace(base, `"observed_at_ms":1792022399000`, `"observed_at_ms":`+number, 1)
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEvent([]byte(body)); err == nil { t.Fatal("invalid event accepted") }
		})
	}
	for _, number := range []string{"1", "999999999999"} {
		body := strings.Replace(base, `"amount_minor":1001`, `"amount_minor":`+number, 1)
		if _, err := ParseEvent([]byte(" \n"+body+"\t")); err != nil { t.Fatal(err) }
	}
}

func TestTransactionIdentityUnicodeScalarsAndUnpairedSurrogates(t *testing.T) {
	v := goldens(t)[0]
	bytes, _ := base64.StdEncoding.DecodeString(v.Body)
	base := strings.TrimSuffix(string(bytes), "}")+`,"provider_transaction_id":`
	for _, tx := range []string{`null`, `""`, `"11111111-1111-4111-8111-111111111111"`, `"\uD800"`, `"\uDC00"`, `"\uD800x"`, `"\uD800\uD800"`, `"bad\u0000control"`, `"`+strings.Repeat("中",129)+`"`} {
		if _, err := ParseEvent([]byte(base+tx+"}")); err == nil { t.Fatalf("invalid transaction string accepted: %s", tx) }
	}
	for _, tx := range []string{`"\uD83D\uDE00"`, `"\uFFFD"`, `"\\uD800"`, `"`+strings.Repeat("中",128)+`"`, `"合成流水"`} {
		if _, err := ParseEvent([]byte(base+tx+"}")); err != nil { t.Fatalf("valid scalar sequence rejected: %s", tx) }
	}
}

func FuzzParseEvent(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"provider_transaction_id":"\uD800"}`))
	f.Add([]byte{255})
	f.Fuzz(func(t *testing.T, body []byte) { _, _ = ParseEvent(body) })
}
