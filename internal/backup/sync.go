// /internal/backup/sync.go
package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// StartBackgroundSync starts all necessary backup goroutines (periodic and/or watcher).
func StartBackgroundSync(ctx context.Context, cfg *config.Config, liveInstanceSaveDir string) {
	if len(cfg.SyncTargets) <= 1 {
		return // Nothing to do, only primary target exists
	}
	backupTargets := cfg.SyncTargets[1:]

	var periodicTargets, watcherTargets []config.SyncTarget
	for _, t := range backupTargets {
		if t.Interval > 0 {
			periodicTargets = append(periodicTargets, t)
		} else if t.Interval == 0 {
			watcherTargets = append(watcherTargets, t)
		}
	}

	if len(periodicTargets) > 0 {
		startPeriodicBackups(ctx, cfg, liveInstanceSaveDir, periodicTargets)
	}
	if len(watcherTargets) > 0 {
		startWatcherBackups(ctx, cfg, liveInstanceSaveDir, watcherTargets)
	}
}

func startPeriodicBackups(ctx context.Context, cfg *config.Config, sourceDir string, targets []config.SyncTarget) {
	log.Log.Info("--- Starting Periodic Background Backups ---")
	sourceTarget := config.SyncTarget{Type: config.Local, Path: sourceDir}
	for _, target := range targets {
		go func(t config.SyncTarget) {
			log.Log.Info("Starting periodic backup for '%s' every %s.", t.Original, t.Interval)
			ticker := time.NewTicker(t.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					log.Log.Info("Periodic backup triggered for '%s'...", t.Original)
					if err := Sync(ctx, cfg, sourceTarget, t); err != nil {
						log.Log.Error("During periodic backup for '%s': %v", t.Original, err)
					}
				case <-ctx.Done():
					log.Log.Info("Stopping periodic backup for '%s'.", t.Original)
					return
				}
			}
		}(target)
	}
}

func startWatcherBackups(ctx context.Context, cfg *config.Config, sourceDir string, targets []config.SyncTarget) {
	log.Log.Info("--- Starting Filesystem Watcher for Backups ---")
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Log.Error("Could not create filesystem watcher: %v", err)
		return
	}

	go func() {
		<-ctx.Done()
		log.Log.Info("Closing filesystem watcher.")
		watcher.Close()
	}()

	err = watcher.Add(sourceDir)
	if err != nil {
		log.Log.Error("Could not watch instance save directory '%s': %v", sourceDir, err)
		return
	}
	log.Log.Info("Watching '%s' for changes to backup.", sourceDir)

	var debounceTimer *time.Timer
	const debounceDuration = 2 * time.Second
	var mu sync.Mutex
	sourceTarget := config.SyncTarget{Type: config.Local, Path: sourceDir}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Log.Info("File change detected: %s. Debouncing backup for %s...", filepath.Base(event.Name), debounceDuration)
					mu.Lock()
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.AfterFunc(debounceDuration, func() {
						log.Log.Info("Debounce timer finished. Triggering backup for all watcher targets.")
						for _, t := range targets {
							if err := Sync(ctx, cfg, sourceTarget, t); err != nil {
								log.Log.Error("During watched backup for '%s': %v", t.Original, err)
							}
						}
					})
					mu.Unlock()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Log.Warn("Watcher error: %v", err)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Sync is the new centralized data synchronization function.
func Sync(ctx context.Context, cfg *config.Config, source, destination config.SyncTarget) error {
	sourcePath := source.Path
	if source.Type == config.Gdrive {
		sourcePath = fmt.Sprintf("%s:%s", source.RemoteName, source.Path)
	}

	destPath := destination.Path
	if destination.Type == config.Gdrive {
		destPath = fmt.Sprintf("%s:%s", destination.RemoteName, destPath)
	}

	log.Log.Info("Syncing from '%s' to '%s'...", sourcePath, destPath)

	// If both are local, we can use a simple directory copy.
	if source.Type == config.Local && destination.Type == config.Local {
		if util.PathExists(destPath) {
			if err := os.RemoveAll(destPath); err != nil {
				return fmt.Errorf("could not clean local destination %s: %w", destPath, err)
			}
		}
		return util.CopyDir(sourcePath, destPath)
	}

	// Otherwise, at least one is remote, so we must use rclone.
	err := RunRcloneCommand(ctx, cfg, "copy", sourcePath, destPath)
	if err != nil {
		return fmt.Errorf("rclone sync from '%s' to '%s' failed: %w", sourcePath, destPath, err)
	}
	log.Log.Info("✅ Sync successful.")
	return nil
}
