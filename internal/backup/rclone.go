// /internal/backup/rclone.go
package backup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"strings"
)

// getRclonePath finds the path to the rclone executable, prioritizing local.
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
		return "", fmt.Errorf("rclone not found locally or in PATH. Dependency check failed")
	}
	return path, nil
}

// RunRcloneCommand executes an rclone command with standardized arguments.
func RunRcloneCommand(cfg *config.Config, args ...string) error {
	rclonePath, err := getRclonePath()
	if err != nil {
		return err
	}

	isQuiet := cfg.LogLevel == "quiet"

	// Prepend the managed config path and other standard flags.
	cmdArgs := []string{"--config", cfg.RcloneConfigPath, "--retries", "5"}
	if !isQuiet {
		cmdArgs = append(cmdArgs, "--progress")
	}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command(rclonePath, cmdArgs...)
	log.Log.Info("Executing: %s", cmd.String())

	if isQuiet {
		// Suppress rclone's output completely in quiet mode.
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

// RunRcloneConfigWizard launches the interactive rclone setup process.
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

// GetConfiguredRemotes runs `rclone listremotes` and returns a map of them.
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
