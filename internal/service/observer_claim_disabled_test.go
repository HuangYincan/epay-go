//go:build !observersynthetic

package service

import (
	"context"
	"errors"
	"github.com/example/epay-go/internal/observerstore"
	"testing"
)

func TestProductionBuildCannotEnableSyntheticOrderWindows(t *testing.T) {
	f := newClaimFixture(t)
	if observerstore.SyntheticEnvironment() != "" {
		t.Fatal("default build accepted synthetic environment")
	}
	_, err := f.service.Create(context.Background(), f.request("must-reject", "wxpay", 0, 0))
	if !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatalf("unexpected result: %v", err)
	}
	f.counts(t, 0, 0)
}
