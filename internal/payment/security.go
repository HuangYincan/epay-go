package payment

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-pay/gopay/pkg/xhttp"
)

// The SDK's default transport skips TLS verification. Replace it before any
// request, including WeChat's platform certificate bootstrap.
func newProviderHTTPClient() *xhttp.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return xhttp.NewClient().SetHttpTransport(transport).SetTimeout(30 * time.Second)
}

// Accept the console's bare Base64 key, PEM keys and PEM certificates, but
// validate first: SDK AutoVerifySign logs malformed keys without returning an error.
func normalizeRSAPublicKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	block, _ := pem.Decode([]byte(value))
	var der []byte
	if block != nil {
		der = block.Bytes
	} else {
		var err error
		der, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
		if err != nil {
			return "", errors.New("支付平台公钥格式无效")
		}
	}
	var key *rsa.PublicKey
	if block != nil && block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(der)
		if err == nil {
			key, _ = cert.PublicKey.(*rsa.PublicKey)
		}
	} else if parsed, err := x509.ParsePKIXPublicKey(der); err == nil {
		key, _ = parsed.(*rsa.PublicKey)
	} else {
		key, _ = x509.ParsePKCS1PublicKey(der)
	}
	if key == nil {
		return "", errors.New("支付平台公钥必须是有效的RSA公钥")
	}
	encoded, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})), nil
}
