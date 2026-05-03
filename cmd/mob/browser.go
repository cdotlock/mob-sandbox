package main

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/cdotlock/mob-sandbox/pkg/config"
)

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	return cmd.Start()
}

func previewDomain(cfg *config.ClientConfig) string {
	domain := cfg.SSHHost
	serverURL := cfg.Server
	serverURL = trimScheme(serverURL)
	if strings.HasPrefix(serverURL, "daytona.") {
		domain = strings.TrimPrefix(serverURL, "daytona.")
	}
	if idx := strings.IndexAny(domain, "/:"); idx > 0 {
		domain = domain[:idx]
	}
	return domain
}

func trimScheme(value string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
			return value[len(prefix):]
		}
	}
	return value
}

func defaultExposeName(sandboxID string) string {
	if len(sandboxID) <= 8 {
		return sandboxID
	}
	return sandboxID[:8]
}
