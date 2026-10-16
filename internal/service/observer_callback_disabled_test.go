//go:build !observersynthetic

package service

import (
	"context"
	"testing"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
)

func TestDefaultBuildRejectsLocalCallbackConfiguration(t *testing.T) {
	endpoint := "https://127.0.0.1:18444/api/user/epay/notify"
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "test")
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_URL", endpoint)
	t.Setenv("OBSERVER_SYNTHETIC_CALLBACK_CA_FILE", "irrelevant-test-file")
	if err := validateMerchantCallback(context.Background(), endpoint, &observerstore.OrderScope{Environment: "test"}); err == nil {
		t.Fatal("default build allowed private callback")
	}
	client, err := syntheticCallbackClient(context.Background(), &model.Order{NotifyURL: endpoint})
	if err != nil || client != nil {
		t.Fatal("default build constructed synthetic client")
	}
}
