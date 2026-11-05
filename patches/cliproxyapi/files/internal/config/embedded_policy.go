package config

import (
	"errors"
	"sync"
)

var embeddedPolicy struct {
	sync.RWMutex
	fn                 func(*Config) error
	loggingDefaultRefs int
}

// InstallEmbeddedPolicy establishes one process-local validation boundary.
func InstallEmbeddedPolicy(fn func(*Config) error) (func(), error) {
	if fn == nil {
		return nil, errors.New("embedded config policy is required")
	}
	embeddedPolicy.Lock()
	defer embeddedPolicy.Unlock()
	if embeddedPolicy.fn != nil {
		return nil, errors.New("embedded config policy already installed")
	}
	embeddedPolicy.fn = fn
	var once sync.Once
	return func() { once.Do(func() { embeddedPolicy.Lock(); embeddedPolicy.fn = nil; embeddedPolicy.Unlock() }) }, nil
}
func ValidateEmbedded(cfg *Config) error {
	embeddedPolicy.RLock()
	defer embeddedPolicy.RUnlock()
	if embeddedPolicy.fn != nil {
		return embeddedPolicy.fn(cfg)
	}
	return nil
}

// EnableEmbeddedLoggingDefaults scopes product defaults to the embedding host.
// Standalone upstream SDK users/tests retain their original console defaults.
func EnableEmbeddedLoggingDefaults() func() {
	embeddedPolicy.Lock()
	embeddedPolicy.loggingDefaultRefs++
	embeddedPolicy.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { embeddedPolicy.Lock(); embeddedPolicy.loggingDefaultRefs--; embeddedPolicy.Unlock() })
	}
}
func applyEmbeddedLoggingDefaults(cfg *Config) {
	embeddedPolicy.RLock()
	enabled := embeddedPolicy.loggingDefaultRefs > 0
	embeddedPolicy.RUnlock()
	if enabled {
		cfg.LoggingToFile = true
		cfg.LogsMaxTotalSizeMB = 100
	}
}
