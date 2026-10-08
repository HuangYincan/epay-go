package sign

import (
	"net/url"
	"testing"
)

func TestMD5SignatureRejectsAmbiguousInput(t *testing.T) {
	params := url.Values{"money": {"1.00"}, "out_trade_no": {"n-1"}}
	signature := GenerateMD5Sign(params, "secret")
	if signature == "" || !VerifyMD5Sign(params, "secret", signature) {
		t.Fatal("valid signature was rejected")
	}
	params.Add("money", "2.00")
	if VerifyMD5Sign(params, "secret", signature) {
		t.Fatal("duplicate signed field was accepted")
	}
}

func TestMD5SignatureRejectsMalformedAndWrongKey(t *testing.T) {
	params := url.Values{"trade_no": {"T1"}}
	if VerifyMD5Sign(params, "secret", "not-a-signature") {
		t.Fatal("malformed signature was accepted")
	}
	signature := GenerateMD5Sign(params, "secret")
	if VerifyMD5Sign(params, "other-secret", signature) {
		t.Fatal("signature with wrong key was accepted")
	}
}
