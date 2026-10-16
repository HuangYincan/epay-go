//go:build !observersynthetic

package observerstore

// Default deployments cannot enable synthetic payment windows through configuration.
func SyntheticEnvironment() string { return "" }
