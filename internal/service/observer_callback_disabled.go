//go:build !observersynthetic

package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/example/epay-go/internal/model"
	"github.com/example/epay-go/internal/observerstore"
	"github.com/example/epay-go/pkg/safehttp"
)

func preflightMerchantCallback(ctx context.Context, raw string) (bool, error) {
	return false, safehttp.ValidateURL(ctx, raw)
}

func validateMerchantCallback(context.Context, string, *observerstore.OrderScope) error {
	return errors.New("synthetic callback disabled")
}

func syntheticCallbackClient(context.Context, *model.Order) (*http.Client, error) {
	return nil, nil
}
