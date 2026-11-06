package buildinfo

import "testing"

func TestPhaseFollowsVersion(t *testing.T) {
	for _, test := range []struct{ version, want string }{
		{"0.4.0", "release"},
		{"1.2.3", "release"},
		{"0.4.0-rc.6", "release-candidate"},
		{"1.0.0-beta.1", "release-candidate"},
		{"development", "development"},
		{"", "development"},
		{"0.4", "development"},
		{"0.4.0\n", "development"},
		{"0.4.0-", "development"},
	} {
		t.Run(test.version, func(t *testing.T) {
			if got := Phase(test.version); got != test.want {
				t.Fatalf("Phase(%q) = %q, want %q", test.version, got, test.want)
			}
		})
	}
}
