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
	"time"
)

// LaunchGame is the main entry point for the launcher logic.
// It determines the run mode and executes the appropriate workflow.
func LaunchGame(cfg *config.Config) error {
	hollowKnightExe := filepath.Join(cfg.HollowKnightInstallPath, "Hollow Knight.exe")
	if !util.PathExists(hollowKnightExe) {
		return fmt.Errorf("executable not found at %s", hollowKnightExe)
	}

	// No targets: simple launch and exit.
	if len(cfg.SyncTargets) == 0 {
		return launchFireAndForget(cfg, hollowKnightExe)
	}

	// Determine if all targets are cloud-based.
	allCloud := true
	for _, t := range cfg.SyncTargets {
		if t.Type != config.Gdrive {
			allCloud = false
			break
		}
	}

	if allCloud {
		return runOnlineOnlyMode(cfg, hollowKnightExe)
	}

	return runIsolatedMode(cfg, hollowKnightExe)
}

// runOnlineOnlyMode manages saves directly in the user's real save directory.
func runOnlineOnlyMode(cfg *config.Config, exePath string) error {
	log.Log.Info("--- Running in Online-Only Mode ---")
	localSavePath := cfg.UserSavePath

	// Pre-launch: Sync down from the newest cloud source if necessary.
	localModTime, _ := util.GetDirLastModTime(localSavePath)
	var newestCloudTarget *config.SyncTarget
	var newestCloudTime time.Time

	for i, target := range cfg.SyncTargets {
		cloudTime, err := backup.GetCloudDirLastModTime(cfg, target)
		if err != nil {
			log.Log.Warn("Could not get mod time for cloud target '%s': %v", target.Original, err)
			continue
		}
		if cloudTime.After(newestCloudTime) {
			newestCloudTime = cloudTime
			newestCloudTarget = &cfg.SyncTargets[i]
		}
	}

	if newestCloudTarget != nil && newestCloudTime.After(localModTime) {
		log.Log.Info("Cloud target '%s' is newer than local saves. Syncing down...", newestCloudTarget.Original)
		remotePath := fmt.Sprintf("%s:%s", newestCloudTarget.RemoteName, newestCloudTarget.Path)
		if err := backup.RunRcloneCommand(cfg, "sync", remotePath, localSavePath); err != nil {
			return fmt.Errorf("failed to sync down from cloud: %w", err)
		}
	} else {
		log.Log.Info("Local saves are up-to-date. Skipping pre-launch sync.")
	}

	// Launch game directly.
	cmd := exec.Command(exePath)
	cmd.Dir = cfg.HollowKnightInstallPath
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Hollow Knight: %w", err)
	}
	log.Log.Info("🚀 Game launched directly. Process ID: %d. Monitoring...", cmd.Process.Pid)

	waitErr := cmd.Wait()
	log.Log.Info("✅ Game process has terminated. Exit code: %v", waitErr)

	// Post-launch: Sync back up to all targets that require it.
	log.Log.Info("--- Syncing saves back to cloud targets ---")
	for _, target := range cfg.SyncTargets {
		doSync := cfg.SyncOnQuit
		if target.SyncOnQuit != nil {
			doSync = *target.SyncOnQuit
		}
		if doSync {
			log.Log.Info("Syncing to '%s' on quit...", target.Original)
			if err := backup.CopyToMaster(cfg, localSavePath, target); err != nil {
				log.Log.Error("Failed syncing back to '%s': %v", target.Original, err)
			} else {
				log.Log.Info("✅ Successfully synced back to '%s'", target.Original)
			}
		}
	}

	return nil
}

// runIsolatedMode finds the latest source and uses a virtualized environment.
func runIsolatedMode(cfg *config.Config, exePath string) error {
	log.Log.Info("--- Running in Isolated Mode ---")

	// Pre-launch: Find the target with the most recent modification time.
	var latestSourceTarget config.SyncTarget
	var latestModTime time.Time
	isFirst := true

	for _, target := range cfg.SyncTargets {
		var currentModTime time.Time
		var err error
		if target.Type == config.Local {
			currentModTime, err = util.GetDirLastModTime(target.Path)
		} else {
			currentModTime, err = backup.GetCloudDirLastModTime(cfg, target)
		}

		if err != nil {
			log.Log.Warn("Could not get mod time for target '%s': %v", target.Original, err)
			continue
		}

		if isFirst || currentModTime.After(latestModTime) {
			latestModTime = currentModTime
			latestSourceTarget = target
			isFirst = false
		}
	}

	log.Log.Info("Latest save source identified: '%s'", latestSourceTarget.Original)

	// Setup and run in isolated environment.
	instanceRoot, instanceSaveDir, err := setupInstanceEnvironment(cfg, latestSourceTarget)
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
	log.Log.Info("🚀 Game launched in isolated environment. Process ID: %d. Monitoring...", cmd.Process.Pid)

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

func setupInstanceEnvironment(cfg *config.Config, sourceTarget config.SyncTarget) (string, string, error) {
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
	log.Log.Info("Populating instance from latest source: %s", sourceTarget.Original)
	if err := copyFromMaster(cfg, sourceTarget, instanceSaveDir); err != nil {
		os.RemoveAll(instanceRoot)
		return "", "", err
	}
	log.Log.Info("✅ Instance environment ready.")
	return instanceRoot, instanceSaveDir, nil
}

// (Other helper functions remain largely the same, but are included for completeness)
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
			log.Log.Error("Failed syncing back to '%s': %v", target.Original, err)
		} else {
			log.Log.Info("✅ Successfully synced back to '%s'", target.Original)
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
