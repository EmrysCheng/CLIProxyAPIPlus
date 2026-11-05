//go:build windows

package main

import "golang.org/x/sys/windows/svc"

func webUpdateRuntimeReason() string {
	service, err := svc.IsWindowsService()
	if err != nil {
		return "Cannot confirm a standalone Windows process; use the offline upgrade tool."
	}
	if service {
		return "Windows Service installations must be upgraded through their service/deployment manager."
	}
	return ""
}
