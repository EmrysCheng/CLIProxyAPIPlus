// Package product serves product-level metadata, never standalone upstream upgrades.
package product

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Asset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}
type Release struct {
	Version           string  `json:"version"`
	SourceCommit      string  `json:"source_commit,omitempty"`
	SourceDigest      string  `json:"product_source_sha256,omitempty"`
	Assets            []Asset `json:"assets"`
	MigrationRequired bool    `json:"migration_required"`
	Notes             string  `json:"notes"`
}
type Index struct {
	SchemaVersion int     `json:"schema_version"`
	Release       Release `json:"release"`
}
type Status struct {
	CurrentVersion    string   `json:"current_version"`
	Configured        bool     `json:"configured"`
	State             string   `json:"state"`
	Latest            *Release `json:"latest,omitempty"`
	LastChecked       string   `json:"last_checked,omitempty"`
	Error             string   `json:"error,omitempty"`
	Automatic         bool     `json:"automatic"`
	Channel           string   `json:"channel"`
	ChannelPreference string   `json:"channel_preference"`
	Stale             bool     `json:"stale"`
	LastSuccess       string   `json:"last_success_at"`
}
type Releases struct {
	URL     string
	Current string
	mu      sync.Mutex
	status  Status
	Client  *http.Client
	Policy  ReleaseSourcePolicy
}

func NewReleases(raw, current string) (*Releases, error) {
	return NewReleasesWithPolicy(raw, current, ReleaseSourcePolicy{})
}

func NewReleasesWithPolicy(raw, current string, policy ReleaseSourcePolicy) (*Releases, error) {
	if err := policy.ValidateIndex(raw); err != nil {
		return nil, err
	}
	r := &Releases{URL: raw, Current: current, Policy: policy}
	r.status = Status{CurrentVersion: current, Configured: raw != "", State: "no_candidate", Channel: "stable", ChannelPreference: "stable", Automatic: raw != ""}
	r.Client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || len(via) > 3 || !policy.AllowsIndexRedirect(raw, req.URL.String()) {
			return errors.New("release redirect is not the configured trusted origin")
		}
		return nil
	}}
	return r, nil
}

// ValidateReleaseIndex validates metadata independently of network/source policy.
func ValidateReleaseIndex(index Index) error {
	if index.SchemaVersion != 1 || !validVersion(index.Release.Version) || !validSource(index.Release) {
		return errors.New("unsupported product release metadata")
	}
	for _, asset := range index.Release.Assets {
		if !validAsset(asset) {
			return errors.New("invalid product asset metadata")
		}
	}
	return nil
}

func validSource(release Release) bool {
	if release.SourceCommit != "" && !commitPattern.MatchString(release.SourceCommit) {
		return false
	}
	if release.SourceDigest != "" && !sourceDigestPattern.MatchString(release.SourceDigest) {
		return false
	}
	return release.SourceCommit != "" || release.SourceDigest != ""
}
func (r *Releases) Snapshot() Status { r.mu.Lock(); defer r.mu.Unlock(); return r.status }
func (r *Releases) Check(ctx context.Context) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.LastChecked = time.Now().UTC().Format(time.RFC3339)
	r.status.Stale = true
	if r.URL == "" {
		r.status.Error = "No product release index configured. Build both upstreams using the verified source workflow."
		return r.status
	}
	req, err := http.NewRequestWithContext(ctx, "GET", r.URL, nil)
	if err != nil {
		r.status.Error = err.Error()
		return r.status
	}
	response, err := r.Client.Do(req)
	if err != nil {
		r.status.Error = "Product release index unavailable"
		return r.status
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		r.status.Error = fmt.Sprintf("release index returned HTTP %d", response.StatusCode)
		return r.status
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		r.status.Error = "release index exceeds size limit"
		return r.status
	}
	var index Index
	if err = json.Unmarshal(data, &index); err != nil || ValidateReleaseIndex(index) != nil {
		r.status.Error = "unsupported product release metadata"
		return r.status
	}
	for _, asset := range index.Release.Assets {
		if !r.Policy.AllowsAsset(r.URL, asset.URL) || !validAsset(asset) {
			r.status.Error = "invalid product asset metadata"
			return r.status
		}
	}
	r.status.Latest = &index.Release
	r.status.Error = ""
	r.status.LastSuccess = r.status.LastChecked
	r.status.Stale = false
	r.status.State = "up_to_date"
	if !validVersion(r.Current) {
		r.status.State = "unknown_current"
	} else if compareVersion(index.Release.Version, r.Current) > 0 {
		r.status.State = "update_available"
	}
	return r.status
}
func (r *Releases) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := req.URL.Path == "/plus-api/releases" || req.URL.Path == "/usage-service/updates"
	check := req.URL.Path == "/plus-api/releases/check" || req.URL.Path == "/usage-service/updates/check" || req.URL.Path == "/usage-service/updates/refresh"
	if !root && !check {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if req.Method == http.MethodPost && check {
		_ = json.NewEncoder(w).Encode(r.Check(req.Context()))
		return
	}
	if req.Method != http.MethodGet || !root {
		w.WriteHeader(405)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "product upgrades require the release workflow; endpoint is read-only"})
		return
	}
	_ = json.NewEncoder(w).Encode(r.Snapshot())
}

var sourceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func validVersion(v string) bool {
	m := versionPattern.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	for _, id := range strings.Split(m[4], ".") {
		if numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}
func numeric(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// compareVersion implements SemVer precedence, ignoring build metadata.
// Callers validate both inputs first; invalid versions must not be advertised.
func compareVersion(a, b string) int {
	x, y := versionPattern.FindStringSubmatch(a), versionPattern.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := compareNumber(x[i], y[i]); c != 0 {
			return c
		}
	}
	if x[4] == y[4] {
		return 0
	}
	if x[4] == "" {
		return 1
	}
	if y[4] == "" {
		return -1
	}
	xs, ys := strings.Split(x[4], "."), strings.Split(y[4], ".")
	for i := 0; i < len(xs) && i < len(ys); i++ {
		u, v := xs[i], ys[i]
		var c int
		switch {
		case numeric(u) && numeric(v):
			c = compareNumber(u, v)
		case numeric(u):
			c = -1
		case numeric(v):
			c = 1
		default:
			c = strings.Compare(u, v)
		}
		if c != 0 {
			return c
		}
	}
	return compareNumber(strings.Repeat("1", len(xs)), strings.Repeat("1", len(ys)))
}
func validAsset(a Asset) bool {
	if a.Name == "" || a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, "/\\\x00\r\n") {
		return false
	}
	hash, err := hex.DecodeString(a.SHA256)
	return err == nil && len(hash) == 32
}

// Run checks operator-configured product releases, never installs automatically.
func (r *Releases) Run(ctx context.Context, interval time.Duration) {
	if r.URL == "" {
		return
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	r.Check(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Check(ctx)
		}
	}
}

// TrustCAFile adds an operator-supplied CA to the dedicated release transport.
// It does not weaken certificate validation or modify process-global transports.
func (r *Releases) TrustCAFile(path string) error {
	pem, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("release CA file contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	r.Client.Transport = transport
	return nil
}

// CompareReleaseVersions exposes the same strict SemVer ordering used for product
// releases. Source checks never turn an older stable tag into an upgrade.
func CompareReleaseVersions(a, b string) (int, error) {
	if !validVersion(a) || !validVersion(b) {
		return 0, errors.New("unsupported release version")
	}
	return compareVersion(a, b), nil
}
