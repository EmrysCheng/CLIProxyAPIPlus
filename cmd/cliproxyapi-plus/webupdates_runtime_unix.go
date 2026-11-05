//go:build !windows

package main

import "os"

func webUpdateRuntimeReason() string {
	for _, name := range []string{"INVOCATION_ID", "SYSTEMD_EXEC_PID", "KUBERNETES_SERVICE_HOST", "container"} {
		if os.Getenv(name) != "" {
			return "Supervised or container deployments must be upgraded through their deployment manager."
		}
	}
	for _, path := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(path); err == nil {
			return "Container deployments must be upgraded by replacing the image."
		}
	}
	return ""
}
