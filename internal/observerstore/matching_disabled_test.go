//go:build !observersynthetic

package observerstore_test

import (
	"errors"
	"github.com/example/epay-go/internal/observerstore"
	"testing"
)

func TestDefaultBuildCannotCreateSyntheticMatcher(t *testing.T) {
	t.Setenv("OBSERVER_SYNTHETIC_ENVIRONMENT", "test")
	db := testDB(t)
	seed(t, db)
	if _, err := observerstore.NewMatcher(db, "test"); !errors.Is(err, observerstore.ErrSyntheticDisabled) {
		t.Fatal("default build loaded matching policy")
	}
}
