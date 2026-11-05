package product

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReleaseSourcePolicy(t *testing.T) {
	local := ReleaseSourcePolicy{AllowLocalHTTP: true}
	for _, raw := range []string{"http://127.0.0.1:26973/index.json", "http://[::1]:26973/index.json", "http://127.0.0.2/index.json"} {
		if err := (ReleaseSourcePolicy{}).ValidateIndex(raw); err == nil {
			t.Fatalf("HTTP without opt-in: %s", raw)
		}
		if err := local.ValidateIndex(raw); err != nil {
			t.Fatalf("loopback rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://localhost/index", "http://localhost.evil/index", "http://192.168.1.2/index", "http://0.0.0.0/index", "http://[::]/index", "http://2130706433/index", "http://127.1/index", "http://user:pass@127.0.0.1/index", "http://127.0.0.1/index#fragment", "http://[::1%25lo]/index", "relative", "file:///index"} {
		if err := local.ValidateIndex(raw); err == nil {
			t.Fatalf("unsafe index accepted: %s", raw)
		}
	}
	index := "http://127.0.0.1:26973/index.json"
	if !local.AllowsAsset(index, "http://127.0.0.1:26973/package.zip") {
		t.Fatal("same-origin local package rejected")
	}
	for _, raw := range []string{"http://127.0.0.1:26974/p", "http://127.0.0.2:26973/p", "http://localhost:26973/p", "http://192.168.1.2/p", "https://127.0.0.1:26973/p", "https://github.com/p", "http://user:pass@127.0.0.1:26973/p"} {
		if local.AllowsAsset(index, raw) || local.AllowsIndexRedirect(index, raw) {
			t.Fatalf("local origin escaped: %s", raw)
		}
	}
	if local.AllowsAsset("https://127.0.0.1:26973/index", "http://127.0.0.1:26973/p") {
		t.Fatal("HTTPS downgrade accepted")
	}
	if !(ReleaseSourcePolicy{}).AllowsAsset("https://releases.example/index", "https://github.com/releases/p") {
		t.Fatal("legacy GitHub package policy regressed")
	}
	if local.AllowsIndexRedirect("https://release.example/index", "https://github.com/index") {
		t.Fatal("index origin redirect accepted")
	}
}

func TestLocalHTTPIndexWithPackageSourceDigest(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Index{SchemaVersion: 1, Release: Release{Version: "1.1.0", SourceDigest: strings.Repeat("a", 64), Assets: []Asset{{Name: "release.zip", SHA256: strings.Repeat("b", 64), URL: server.URL + "/release.zip"}}}})
	}))
	defer server.Close()
	if _, err := NewReleases(server.URL+"/index.json", "1.0.0"); err == nil {
		t.Fatal("HTTP accepted without opt-in")
	}
	releases, err := NewReleasesWithPolicy(server.URL+"/index.json", "1.0.0", ReleaseSourcePolicy{AllowLocalHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if status := releases.Check(context.Background()); status.Error != "" || status.State != "update_available" {
		t.Fatalf("local check: %+v", status)
	}
}

func TestLocalHTTPIndexRedirectCannotLeaveOrigin(t *testing.T) {
	var contacts atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacts.Add(1) }))
	defer trap.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, trap.URL+"/index", http.StatusFound) }))
	defer server.Close()
	releases, err := NewReleasesWithPolicy(server.URL+"/index", "1.0.0", ReleaseSourcePolicy{AllowLocalHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if status := releases.Check(context.Background()); status.Error == "" {
		t.Fatal("redirect should fail closed")
	}
	if contacts.Load() != 0 {
		t.Fatal("cross-origin server contacted")
	}
}

func TestReleaseSourceIdentity(t *testing.T) {
	for _, release := range []Release{{}, {SourceCommit: "made-up"}, {SourceDigest: "development"}, {SourceCommit: strings.Repeat("a", 40), SourceDigest: "bad"}} {
		if validSource(release) {
			t.Fatalf("invalid source: %+v", release)
		}
	}
	for _, release := range []Release{{SourceCommit: strings.Repeat("a", 40)}, {SourceDigest: strings.Repeat("b", 64)}, {SourceCommit: strings.Repeat("a", 40), SourceDigest: strings.Repeat("b", 64)}} {
		if !validSource(release) {
			t.Fatalf("valid source rejected: %+v", release)
		}
	}
}
