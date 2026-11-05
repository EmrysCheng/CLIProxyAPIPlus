package sourceupdate

import "testing"

func TestManagerNodeEngineMatchesLockedRequirements(t *testing.T) {
	for _, version := range []string{"v22.23.4", "v22.24.0", "v24.22.1", "v24.23.0", "v26.12.1", "v27.0.0"} {
		if !supportedNode(version) {
			t.Fatal("supported Node rejected", version)
		}
	}
	for _, version := range []string{"v20.19.0", "v22.0.0", "v22.23.3", "v24.22.0", "v25.99.0", "v26.12.0", "v24", "node"} {
		if supportedNode(version) {
			t.Fatal("unsupported Node allowed", version)
		}
	}
}
