package provider

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed embedded
var embeddedFiles embed.FS

// extractNetworkRunner extracts the embedded network-runner binary to
// cacheDir/bin/network-runner and returns its path. Falls back to PATH lookup
// when built without `make build` (e.g. during development with `go build`).
func extractNetworkRunner(cacheDir string) (string, error) {
	binaryPath := filepath.Join(cacheDir, "bin", "network-runner")
	return extractEmbedded("embedded/network-runner", binaryPath, "network-runner")
}

// extractVfkit extracts the embedded vfkit binary to cacheDir/bin/vfkit and
// returns its path. Falls back to PATH lookup when built without `make build`.
func extractVfkit(cacheDir string) (string, error) {
	return extractEmbedded("embedded/vfkit", filepath.Join(cacheDir, "bin", "vfkit"), "vfkit")
}

func extractEmbedded(embeddedPath, destPath, fallbackName string) (string, error) {
	data, err := embeddedFiles.ReadFile(embeddedPath)
	if err != nil {
		return exec.LookPath(fallbackName)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}

	tmp := destPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0755); err != nil {
		return "", fmt.Errorf("failed to write %s binary: %w", fallbackName, err)
	}
	if err := os.Rename(tmp, destPath); err != nil {
		return "", fmt.Errorf("failed to install %s binary: %w", fallbackName, err)
	}
	return destPath, nil
}
