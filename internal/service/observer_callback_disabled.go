//go:build !observersynthetic

package service

import (
	"context"
	"net/http"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/safehttp"
)

func validateMerchantCallback(ctx context.Context, raw string, _ *observerstore.OrderScope) error {
	return safehttp.ValidateURL(ctx, raw)
}

func syntheticCallbackClient(context.Context, *model.Order) (*http.Client, error) {
	return nil, nil
}
