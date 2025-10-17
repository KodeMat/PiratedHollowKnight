// /internal/backup/rclone.go
package backup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"strings"
	"time"
)

// rcloneLsjsonItem represents a single item in the output of `rclone lsjson`.
type rcloneLsjsonItem struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

// GetCloudDirLastModTime fetches the most recent modification time from a cloud directory.
func GetCloudDirLastModTime(ctx context.Context, cfg *config.Config, target config.SyncTarget) (time.Time, error) {
	rclonePath, err := getRclonePath()
	if err != nil {
		return time.Time{}, err
	}

	remotePath := fmt.Sprintf("%s:%s", target.RemoteName, target.Path)
	cmdArgs := []string{"--config", cfg.RcloneConfigPath, "lsjson", remotePath}
	cmd := exec.CommandContext(ctx, rclonePath, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Specific check for directory not found, which is not a fatal error.
		if strings.Contains(stderr.String(), "directory not found") {
			return time.Time{}, nil // Return zero time, indicating it doesn't exist yet.
		}
		return time.Time{}, fmt.Errorf("rclone lsjson failed for %s: %w\nOutput: %s", remotePath, err, stderr.String())
	}

	var items []rcloneLsjsonItem
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse rclone lsjson output: %w", err)
	}

	var latestModTime time.Time
	for _, item := range items {
		if item.ModTime.After(latestModTime) {
			latestModTime = item.ModTime
		}
	}

	return latestModTime, nil
}

// (Rest of file is unchanged)
func getRclonePath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not determine application directory: %w", err)
	}
	localRclonePath := filepath.Join(filepath.Dir(exePath), "rclone.exe")
	if util.PathExists(localRclonePath) {
		return localRclonePath, nil
	}
	path, err := exec.LookPath("rclone")
	if err != nil {
		return "", fmt.Errorf("rclone not found locally or in PATH")
	}
	return path, nil
}
func RunRcloneCommand(ctx context.Context, cfg *config.Config, args ...string) error {
	rclonePath, err := getRclonePath()
	if err != nil {
		return err
	}
	isQuiet := cfg.LogLevel == "quiet"
	cmdArgs := []string{"--config", cfg.RcloneConfigPath, "--retries", "5"}
	if !isQuiet {
		cmdArgs = append(cmdArgs, "--progress")
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.CommandContext(ctx, rclonePath, cmdArgs...)
	log.Log.Info("Executing: %s", cmd.String())
	if isQuiet {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("rclone command failed: %w", err)
	}
	log.Log.Info("rclone command completed successfully.")
	return nil
}
func RunRcloneConfigWizard(cfg *config.Config) error {
	rclonePath, err := getRclonePath()
	if err != nil {
		return fmt.Errorf("could not find rclone.exe to run setup: %w", err)
	}
	log.Log.Prompt("The official rclone configuration wizard will now start.")
	log.Log.Prompt("Please follow the on-screen instructions.")
	cmd := exec.Command(rclonePath, "config", "--config", cfg.RcloneConfigPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func GetConfiguredRemotes(cfg *config.Config) (map[string]bool, error) {
	rclonePath, err := getRclonePath()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(rclonePath, "listremotes", "--config", cfg.RcloneConfigPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w\nOutput: %s", err, string(output))
	}
	remotes := make(map[string]bool)
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	for _, line := range lines {
		if strings.HasSuffix(line, ":") {
			remoteName := strings.TrimSuffix(line, ":")
			remotes[remoteName] = true
		}
	}
	return remotes, nil
}
