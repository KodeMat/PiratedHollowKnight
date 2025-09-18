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
	for _, target := range targets {
		go func(t config.SyncTarget) {
			log.Log.Info("Starting periodic backup for '%s' every %s.", t.Original, t.Interval)
			ticker := time.NewTicker(t.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					log.Log.Info("Periodic backup triggered for '%s'...", t.Original)
					if err := CopyToMaster(cfg, sourceDir, t); err != nil {
						log.Log.Error("During periodic backup for '%s': %v", t.Original, err)
					} else {
						log.Log.Info("✅ Periodic backup for '%s' successful.", t.Original)
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
							if err := CopyToMaster(cfg, sourceDir, t); err != nil {
								log.Log.Error("During watched backup for '%s': %v", t.Original, err)
							} else {
								log.Log.Info("✅ Watched backup for '%s' successful.", t.Original)
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

// CopyToMaster is a data synchronization function. It moves data from the temporary
// instance directory to a final master/backup destination.
func CopyToMaster(cfg *config.Config, instanceDir string, master config.SyncTarget) error {
	switch master.Type {
	case config.Local:
		if util.PathExists(master.Path) {
			if err := os.RemoveAll(master.Path); err != nil {
				return fmt.Errorf("could not clean local destination %s: %w", master.Path, err)
			}
		}
		return util.CopyDir(instanceDir, master.Path)
	case config.Gdrive:
		remotePath := fmt.Sprintf("%s:%s", master.RemoteName, master.Path)
		return RunRcloneCommand(cfg, "copy", instanceDir, remotePath)
	}
	return fmt.Errorf("unknown master target type: %v", master.Type)
}
