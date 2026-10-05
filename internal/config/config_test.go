package config

import "testing"

func TestRejectUnsafeJWT(t *testing.T) {
	for _, secret := range []string{"", "short", "your-super-secret-key-change-in-production", "your_jwt_secret_here_change_in_production"} {
		if err := ValidateJWT(JWTConfig{Secret: secret, ExpireHour: 1}); err == nil {
			t.Fatal("accepted insecure JWT config")
		}
	}
	if err := ValidateJWT(JWTConfig{Secret: "fd0c3b1478a7432f8f0a442063ec73e9", ExpireHour: 1}); err != nil {
		t.Fatal(err)
	}
}
