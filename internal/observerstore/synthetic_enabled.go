//go:build observersynthetic

package observerstore

import "os"

// This build is exclusively for an isolated synthetic prototype. Database scope
// and merchant allowlists are checked separately on every use.
func SyntheticEnvironment() string { return os.Getenv("OBSERVER_SYNTHETIC_ENVIRONMENT") }
