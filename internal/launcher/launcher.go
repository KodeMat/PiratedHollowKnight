// /internal/launcher/launcher.go
package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"pirated-hollow-knight/internal/backup"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"syscall"
	"time"
)

// LaunchGame is the main entry point for the new "Transactional Swap" launcher logic.
func LaunchGame(cfg *config.Config) error {
	hollowKnightExe := filepath.Join(cfg.HollowKnightInstallPath, "Hollow Knight.exe")
	if !util.PathExists(hollowKnightExe) {
		return fmt.Errorf("executable not found at %s", hollowKnightExe)
	}

	// If no targets are specified, just launch the game normally.
	if len(cfg.SyncTargets) == 0 {
		return launchFireAndForget(cfg, hollowKnightExe)
	}

	// --- Transactional Swap Logic Begins ---

	// 1. Acquire Lock
	lockFilePath, err := acquireLock()
	if err != nil {
		return err
	}
	defer releaseLock(lockFilePath)

	// 2. Backup Real Saves
	realSavePath := cfg.UserSavePath
	backupPath, err := backupRealSaves(realSavePath)
	if err != nil {
		return fmt.Errorf("failed to backup real saves: %w", err)
	}
	// Defer the restoration of the real saves to ensure it always runs.
	defer restoreRealSaves(backupPath, realSavePath)

	// 3. Identify Latest Source
	latestSourceTarget, err := findLatestSource(cfg)
	if err != nil {
		return fmt.Errorf("could not determine latest save source: %w", err)
	}
	log.Log.Info("Latest save source identified: '%s'", latestSourceTarget.Original)

	// 4. Swap In (Populate the real save directory)
	if err := copyFromMaster(cfg, latestSourceTarget, realSavePath); err != nil {
		return fmt.Errorf("failed to swap in saves from '%s': %w", latestSourceTarget.Original, err)
	}
	log.Log.Info("Successfully populated real save directory from latest source.")

	// 5. Launch Game
	cmd := exec.Command(hollowKnightExe)
	cmd.Dir = cfg.HollowKnightInstallPath
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to launch Hollow Knight: %w", err)
	}
	log.Log.Info("🚀 Game launched. Process ID: %d. Waiting for exit...", cmd.Process.Pid)

	// Intercept Ctrl+C to ensure cleanup still happens
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Log.Warn("\n🚨 Interrupt signal received. Terminating game process...")
		_ = cmd.Process.Kill()
	}()

	// 6. Wait for Exit
	waitErr := cmd.Wait()
	log.Log.Info("✅ Game process has terminated. Exit code: %v", waitErr)

	// 7. Swap Out (Copy saves back to their origin)
	log.Log.Info("Copying session saves back to '%s'...", latestSourceTarget.Original)
	if err := backup.CopyToMaster(cfg, realSavePath, latestSourceTarget); err != nil {
		return fmt.Errorf("failed to swap out saves to '%s': %w", latestSourceTarget.Original, err)
	}
	log.Log.Info("✅ Save data successfully synced back.")

	// 8 & 9 (Restore and Release Lock) are handled by the deferred calls.
	return nil
}

func acquireLock() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	lockFilePath := filepath.Join(filepath.Dir(exePath), "hk.lock")

	if util.PathExists(lockFilePath) {
		return "", errors.New("lock file 'hk.lock' exists. Another instance may be running. If not, please delete the file manually")
	}

	if err := os.WriteFile(lockFilePath, []byte("locked"), 0644); err != nil {
		return "", fmt.Errorf("could not create lock file: %w", err)
	}
	log.Log.Info("Acquired instance lock.")
	return lockFilePath, nil
}

func releaseLock(lockFilePath string) {
	if err := os.Remove(lockFilePath); err != nil {
		log.Log.Warn("Failed to remove lock file '%s': %v", lockFilePath, err)
	} else {
		log.Log.Info("Released instance lock.")
	}
}

func backupRealSaves(realSavePath string) (string, error) {
	if !util.PathExists(realSavePath) {
		log.Log.Info("Real save directory does not exist, no backup needed.")
		return "", nil // Nothing to back up
	}

	backupPath, err := os.MkdirTemp("", "hk-realsave-backup-*")
	if err != nil {
		return "", err
	}

	log.Log.Info("Backing up current saves from '%s' to '%s'", realSavePath, backupPath)
	if err := util.CopyDir(realSavePath, backupPath); err != nil {
		return "", err
	}
	if err := os.RemoveAll(realSavePath); err != nil {
		return "", err
	}
	return backupPath, nil
}

func restoreRealSaves(backupPath, realSavePath string) {
	if backupPath == "" {
		return // Nothing was backed up.
	}
	log.Log.Info("Restoring original saves to '%s'", realSavePath)
	// Clean the directory first in case the game created new files.
	_ = os.RemoveAll(realSavePath)
	if err := util.CopyDir(backupPath, realSavePath); err != nil {
		log.Log.Error("CRITICAL: Failed to restore original saves: %v", err)
	}
	_ = os.RemoveAll(backupPath) // Clean up the backup dir.
}

func findLatestSource(cfg *config.Config) (config.SyncTarget, error) {
	var latestSourceTarget config.SyncTarget
	var latestModTime time.Time
	foundAny := false

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

		if !foundAny || currentModTime.After(latestModTime) {
			latestModTime = currentModTime
			latestSourceTarget = target
			foundAny = true
		}
	}

	if !foundAny {
		return config.SyncTarget{}, errors.New("could not find any valid/accessible save targets")
	}

	return latestSourceTarget, nil
}

func copyFromMaster(cfg *config.Config, master config.SyncTarget, destDir string) error {
	// Ensure the destination exists and is empty
	_ = os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	switch master.Type {
	case config.Local:
		if !util.PathExists(master.Path) {
			log.Log.Warn("Source path '%s' doesn't exist. Starting with an empty save directory.", master.Path)
			return nil
		}
		return util.CopyDir(master.Path, destDir)
	case config.Gdrive:
		remotePath := fmt.Sprintf("%s:%s", master.RemoteName, master.Path)
		// Use copy here as we are populating a directory.
		return backup.RunRcloneCommand(cfg, "copy", remotePath, destDir)
	}
	return fmt.Errorf("unknown master target type: %v", master.Type)
}

// --- Unchanged Functions ---

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
