// Package buildinfo describes the release channel without implying production acceptance.
package buildinfo

import "regexp"

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var prereleaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+$`)

// Phase follows the built version, rather than permanently labeling every binary a candidate.
// An unversioned local go build remains a development build.
func Phase(version string) string {
	if releaseVersion.MatchString(version) {
		return "release"
	}
	if prereleaseVersion.MatchString(version) {
		return "release-candidate"
	}
	return "development"
}
