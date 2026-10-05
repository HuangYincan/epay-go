package payment

import (
	"fmt"
	"strings"
)

func CanonicalMethod(plugin, method string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "", "scan", "qrcode", "native", "precreate":
		if plugin == "alipay" {
			return "qrcode"
		}
		return "native"
	case "wap", "h5":
		if plugin == "alipay" {
			return "wap"
		}
		return "h5"
	case "web", "pc", "page":
		return "page"
	}
	return method
}

func CheckMethod(plugin, enabled, method string) error {
	method = CanonicalMethod(plugin, method)
	supported := map[string]string{"alipay": "qrcode,wap,page", "wechat": "native,h5,jsapi", "hf-alipay": "native", "hf-wxpay": "native,h5,jsapi"}
	// Third-party adapters must still explicitly list their enabled methods.
	if list, ok := supported[plugin]; ok && !containsMethod(plugin, list, method) {
		return fmt.Errorf("支付通道不支持接口 %s", method)
	}
	if !containsMethod(plugin, enabled, method) {
		return fmt.Errorf("通道未启用支付接口 %s", method)
	}
	return nil
}
func containsMethod(plugin, list, method string) bool {
	for _, v := range strings.Split(list, ",") {
		if strings.TrimSpace(v) != "" && CanonicalMethod(plugin, v) == method {
			return true
		}
	}
	return false
}
