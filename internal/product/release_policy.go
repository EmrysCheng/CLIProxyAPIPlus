package product

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// ReleaseSourcePolicy is operator configuration, never accepted from web requests.
// Local HTTP is restricted to literal loopback IPs: DNS names cannot rebind it.
type ReleaseSourcePolicy struct {
	AllowLocalHTTP bool
}

func parseReleaseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, false
	}
	return u, true
}

func literalLoopback(u *url.URL) bool {
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.Zone() == "" && ip.IsLoopback()
}

func (p ReleaseSourcePolicy) ValidateIndex(raw string) error {
	if raw == "" {
		return nil
	}
	u, ok := parseReleaseURL(raw)
	if ok && (u.Scheme == "https" || (p.AllowLocalHTTP && u.Scheme == "http" && literalLoopback(u))) {
		return nil
	}
	return fmt.Errorf("release index must be an absolute HTTPS URL without credentials; local HTTP requires --allow-local-release-http and a literal loopback IP (127.0.0.1 or ::1)")
}

func sameReleaseOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}

// AllowsIndexRedirect forbids origin changes and HTTPS downgrades, including
// when local HTTP was explicitly enabled for a different installation.
func (p ReleaseSourcePolicy) AllowsIndexRedirect(index, raw string) bool {
	if index == "" || p.ValidateIndex(index) != nil {
		return false
	}
	base, _ := parseReleaseURL(index)
	target, ok := parseReleaseURL(raw)
	return ok && sameReleaseOrigin(base, target)
}

func (p ReleaseSourcePolicy) AllowsAsset(index, raw string) bool {
	if index == "" || p.ValidateIndex(index) != nil {
		return false
	}
	base, _ := parseReleaseURL(index)
	target, ok := parseReleaseURL(raw)
	if !ok {
		return false
	}
	if base.Scheme == "http" {
		// No other ports, host aliases, HTTPS origins or GitHub redirects in local mode.
		return target.Scheme == "http" && literalLoopback(target) && sameReleaseOrigin(base, target)
	}
	if target.Scheme != "https" {
		return false
	}
	if sameReleaseOrigin(base, target) {
		return true
	}
	switch strings.ToLower(target.Host) {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
