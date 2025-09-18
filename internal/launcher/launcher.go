// /internal/launcher/launcher.go
package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"pirated-hollow-knight/internal/backup"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"strings"
	"syscall"
)

// LaunchGame handles the primary logic of preparing the environment and running the game.
func LaunchGame(cfg *config.Config) error {
	hollowKnightExe := filepath.Join(cfg.HollowKnightInstallPath, "hollow_knight.exe")
	if !util.PathExists(hollowKnightExe) {
		return fmt.Errorf("executable not found at %s", hollowKnightExe)
	}

	if len(cfg.SyncTargets) == 0 {
		return launchFireAndForget(cfg, hollowKnightExe)
	}

	return launchIsolated(cfg, hollowKnightExe)
}

func launchFireAndForget(cfg *config.Config, exePath string) error {
	log.Log.Info("No save targets specified. Launching game and detaching.")
	cmd := exec.Command(exePath)
	cmd.Dir = cfg.HollowKnightInstallPath
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Hollow Knight: %w", err)
	}
	log.Log.Info("✅ Game launched successfully. This program will now exit.")
	return nil
}

func launchIsolated(cfg *config.Config, exePath string) error {
	instanceRoot, instanceSaveDir, err := setupInstanceEnvironment(cfg)
	if err != nil {
		return fmt.Errorf("failed to set up isolated game environment: %w", err)
	}
	defer cleanupInstanceEnvironment(instanceRoot)

	cmd := exec.Command(exePath)
	cmd.Dir = cfg.HollowKnightInstallPath
	cmd.Env = getModifiedEnv(instanceRoot)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Hollow Knight: %w", err)
	}

	log.Log.Info("🚀 Launching %s...", exePath)
	log.Log.Info("✅ Game launched successfully in isolated environment. Process ID: %d. Monitoring...", cmd.Process.Pid)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backup.StartBackgroundSync(ctx, cfg, instanceSaveDir)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Log.Warn("\n🚨 Interrupt signal received. Terminating game process...")
		_ = cmd.Process.Kill()
		cancel()
	}()

	waitErr := cmd.Wait()
	log.Log.Info("✅ Game process has terminated. Exit code: %v", waitErr)

	copySavesBack(cfg, instanceSaveDir)
	return nil
}

func setupInstanceEnvironment(cfg *config.Config) (string, string, error) {
	log.Log.Info("--- Setting up isolated game instance ---")
	instanceRoot, err := os.MkdirTemp("", "hk-instance-root-*")
	if err != nil {
		return "", "", err
	}
	instanceSaveDir := filepath.Join(instanceRoot, "AppData", "LocalLow", "Team Cherry", "Hollow Knight")
	if err := os.MkdirAll(instanceSaveDir, 0755); err != nil {
		os.RemoveAll(instanceRoot)
		return "", "", err
	}
	log.Log.Info("Created instance save directory: %s", instanceSaveDir)

	primaryTarget := cfg.SyncTargets[0]
	log.Log.Info("Populating instance from primary target: %s", primaryTarget.Path)
	if err := copyFromMaster(cfg, primaryTarget, instanceSaveDir); err != nil {
		os.RemoveAll(instanceRoot)
		return "", "", err
	}
	log.Log.Info("✅ Instance environment ready.")
	return instanceRoot, instanceSaveDir, nil
}

func copySavesBack(cfg *config.Config, instanceSaveDir string) {
	log.Log.Info("--- Copying saves back to all configured targets ---")
	for _, target := range cfg.SyncTargets {
		doSync := cfg.SyncOnQuit
		if target.SyncOnQuit != nil {
			doSync = *target.SyncOnQuit
		}
		if !doSync {
			log.Log.Info("Skipping quit-sync for '%s' as per configuration.", target.Original)
			continue
		}
		if err := backup.CopyToMaster(cfg, instanceSaveDir, target); err != nil {
			log.Log.Error("Failed syncing back to '%s': %v", target.Path, err)
		} else {
			log.Log.Info("✅ Successfully synced back to '%s'", target.Path)
		}
	}
}

func cleanupInstanceEnvironment(instanceRoot string) {
	log.Log.Info("--- Tearing down isolated game instance ---")
	log.Log.Info("Deleting instance root directory: %s", instanceRoot)
	if err := os.RemoveAll(instanceRoot); err != nil {
		log.Log.Warn("Failed to delete instance root directory: %v", err)
	}
	log.Log.Info("✅ Teardown complete.")
}

func getModifiedEnv(instanceRoot string) []string {
	env := os.Environ()
	var newEnv []string
	for _, e := range env {
		if !strings.HasPrefix(strings.ToUpper(e), "USERPROFILE=") {
			newEnv = append(newEnv, e)
		}
	}
	return append(newEnv, "USERPROFILE="+instanceRoot)
}

func copyFromMaster(cfg *config.Config, master config.SyncTarget, instanceDir string) error {
	switch master.Type {
	case config.Local:
		if !util.PathExists(master.Path) {
			log.Log.Warn("Master path %s doesn't exist, starting with empty save directory.", master.Path)
			return nil
		}
		return util.CopyDir(master.Path, instanceDir)
	case config.Gdrive:
		remotePath := fmt.Sprintf("%s:%s", master.RemoteName, master.Path)
		return backup.RunRcloneCommand(cfg, "copy", remotePath, instanceDir)
	}
	return fmt.Errorf("unknown master target type: %v", master.Type)
}

func RunClean(cfg *config.Config) error {
	log.Log.Info("--- Running Clean Mode ---")
	if util.PathExists(cfg.HollowKnightInstallPath) {
		log.Log.Info("Removing Hollow Knight installation from: %s", cfg.HollowKnightInstallPath)
		if err := os.RemoveAll(cfg.HollowKnightInstallPath); err != nil {
			return err
		}
		log.Log.Info("✅ Hollow Knight directory removed.")
	}
	exePath, _ := os.Executable()
	localRclonePath := filepath.Join(filepath.Dir(exePath), "rclone.exe")
	if util.PathExists(localRclonePath) {
		log.Log.Info("Removing downloaded rclone.exe from: %s", localRclonePath)
		if err := os.Remove(localRclonePath); err != nil {
			return err
		}
		log.Log.Info("✅ rclone.exe removed.")
	}
	log.Log.Warn("Note: 'rclone.conf' is not removed to preserve your configuration.")
	log.Log.Info("--- Clean-up complete ---")
	return nil
}
