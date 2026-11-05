// Package cpatransport injects the embedded CPA transport without changing net/http globals.
package cpatransport

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

var state struct {
	sync.RWMutex
	transport http.RoundTripper
}

// Install is process-scoped: an embedded Manager owns the override until shutdown.
func Install(transport http.RoundTripper) (func(), error) {
	if transport == nil {
		return nil, errors.New("embedded CPA transport is required")
	}
	state.Lock()
	defer state.Unlock()
	if state.transport != nil {
		return nil, errors.New("embedded CPA transport already installed")
	}
	state.transport = transport
	var once sync.Once
	return func() { once.Do(func() { state.Lock(); state.transport = nil; state.Unlock() }) }, nil
}

func Transport() http.RoundTripper {
	state.RLock()
	defer state.RUnlock()
	if state.transport != nil {
		return state.transport
	}
	return http.DefaultTransport
}

func NewClient(timeout time.Duration) *http.Client {
	state.RLock()
	embedded := state.transport != nil
	state.RUnlock()
	client := &http.Client{Timeout: timeout, Transport: Transport()}
	if embedded {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || len(via) >= 5 || req.URL.User != nil ||
				req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
				return errors.New("embedded CPA redirect target is not permitted")
			}
			return nil
		}
	}
	return client
}
