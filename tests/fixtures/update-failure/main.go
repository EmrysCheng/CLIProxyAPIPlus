// update-failure is a test-only release fixture. It is never packaged as a product.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var Version = "9.9.9"
var SourceDigest = "development"

func main() {
	for i, arg := range os.Args[1:] {
		if arg == "--version" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"version": Version, "phase": "release-candidate", "os": runtime.GOOS, "arch": runtime.GOARCH, "web_update_protocol": 1, "local_release_http": true, "source_update_protocol": 1, "product_source_sha256": SourceDigest, "core_commit": strings.Repeat("a", 40), "manager_commit": strings.Repeat("b", 40)})
			return
		}
		if arg == "--data-dir" && i+2 < len(os.Args) {
			_ = os.WriteFile(filepath.Join(os.Args[i+2], "migration-touched"), []byte("failed migration"), 0600)
		}
	}
	os.Exit(17)
}
