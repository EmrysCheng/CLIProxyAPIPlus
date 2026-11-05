package product

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type trip func(*http.Request) (*http.Response, error)

func (f trip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReleaseIndex(t *testing.T) {
	r, err := NewReleases("https://releases.example/index.json", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	r.Client = &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"schema_version":1,"release":{"source_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"1.1.0","assets":[]}}`))}, nil
	})}
	status := r.Check(context.Background())
	if status.State != "update_available" || status.Latest.Version != "1.1.0" {
		t.Fatal(status)
	}
}
func TestRejectUnsafeIndex(t *testing.T) {
	for _, raw := range []string{"http://example/index", "https://user:secret@example/index", "relative"} {
		if _, err := NewReleases(raw, "1"); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestUnconfiguredDoesNotFetch(t *testing.T) {
	r, _ := NewReleases("", "1")
	status := r.Check(context.Background())
	if status.Configured || status.Latest != nil || status.Error == "" {
		t.Fatal(status)
	}
}

func TestSemverPrecedence(t *testing.T) {
	cases := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "2.0.0"}
	for i := 1; i < len(cases); i++ {
		if !validVersion(cases[i]) || compareVersion(cases[i-1], cases[i]) >= 0 {
			t.Fatal(cases[i])
		}
	}
	for _, v := range []string{"1.1", "1.0.0-01", "01.0.0", "evil", "1.0.0-"} {
		if validVersion(v) {
			t.Fatal(v)
		}
	}
	if compareVersion("v1.0.0+abc", "1.0.0+xyz") != 0 {
		t.Fatal("build metadata affects precedence")
	}
}
func TestAssetValidation(t *testing.T) {
	for _, a := range []Asset{{Name: "..", SHA256: strings.Repeat("a", 64)}, {Name: "x.zip", SHA256: strings.Repeat("z", 64)}, {Name: "", SHA256: strings.Repeat("a", 64)}} {
		if validAsset(a) {
			t.Fatal(a)
		}
	}
}
func TestOlderIndexNeverAdvertisesDowngrade(t *testing.T) {
	r, _ := NewReleases("https://release.example/index.json", "2.0.0")
	r.Client = &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"schema_version":1,"release":{"source_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"1.9.0"}}`))}, nil
	})}
	if r.Check(context.Background()).State != "up_to_date" {
		t.Fatal("advertised downgrade")
	}
}

func TestFailedCheckMarksCachedMetadataStale(t *testing.T) {
	r, _ := NewReleases("https://release.example/index.json", "1.0.0")
	r.Client = &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"schema_version":1,"release":{"source_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"1.1.0","assets":[]}}`))}, nil
	})}
	first := r.Check(context.Background())
	if first.Stale || !first.Automatic || first.State != "update_available" {
		t.Fatal(first)
	}
	r.Client = &http.Client{Transport: trip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}
	second := r.Check(context.Background())
	if !second.Stale || second.Error == "" || second.Latest == nil {
		t.Fatal(second)
	}
}
