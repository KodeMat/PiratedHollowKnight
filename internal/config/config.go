// /internal/config/config.go
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// (optionalInt custom flag type remains the same)
type optionalInt int

func (i *optionalInt) String() string {
	if *i == -1 {
		return "infinite"
	}
	return strconv.Itoa(int(*i))
}
func (i *optionalInt) Set(value string) error {
	if value == "true" {
		*i = -1
		return nil
	}
	val, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	*i = optionalInt(val)
	return nil
}
func (i *optionalInt) IsBoolFlag() bool { return true }

// Config holds all application settings.
type Config struct {
	HollowKnightInstallPath string
	UserSavePath            string
	SyncTargets             []SyncTarget
	SyncOnQuit              bool
	DownloadRetries         optionalInt
	RcloneConfigPath        string
	ForceRcloneAuth         bool
	LogLevel                string
	RunClean                bool
}

type SyncType int

const (
	Local SyncType = iota
	Gdrive
)

type SyncTarget struct {
	Type       SyncType
	Path       string
	RemoteName string
	Interval   time.Duration
	SyncOnQuit *bool
	Original   string
}

type stringSlice []string

func (s *stringSlice) String() string         { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(value string) error { *s = append(*s, value); return nil }

func Load() (*Config, error) {
	fs := flag.NewFlagSet("main", flag.ExitOnError)
	cfg := &Config{}
	var targets stringSlice
	var installPath string
	cfg.DownloadRetries = 1

	fs.Var(&targets, "target", "Master/backup save location. Repeatable. Format: \"path|interval|quit_sync\"")
	fs.BoolVar(&cfg.SyncOnQuit, "sync-on-quit", false, "Globally enable sync on game exit for targets without a 'quit' option.")
	fs.StringVar(&installPath, "install-path", "", "Path to the Hollow Knight game installation directory. Defaults to user's Documents/Hollow Knight.")
	fs.Var(&cfg.DownloadRetries, "download-retries", "Number of times to retry download. If flag is present without a value, retries are infinite.")
	fs.StringVar(&cfg.RcloneConfigPath, "config-path", "", "Path to the rclone.conf file. Defaults to 'rclone.conf' in the executable's directory.")
	fs.BoolVar(&cfg.ForceRcloneAuth, "auth", false, "Force the rclone authentication wizard to run for online targets.")
	fs.StringVar(&cfg.LogLevel, "log-level", "quiet", "Set logging verbosity. Options: info, warn, error, quiet.")
	fs.Parse(os.Args[1:])

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine user home directory: %w", err)
	}
	cfg.UserSavePath = filepath.Join(homeDir, "AppData", "LocalLow", "Team Cherry", "Hollow Knight")

	if installPath == "" {
		cfg.HollowKnightInstallPath = filepath.Join(homeDir, "Documents", "Hollow Knight")
	} else {
		cfg.HollowKnightInstallPath = installPath
	}

	if cfg.RcloneConfigPath == "" {
		exePath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("could not determine application directory: %w", err)
		}
		cfg.RcloneConfigPath = filepath.Join(filepath.Dir(exePath), "rclone.conf")
	}

	for i, t := range targets {
		target := parseTargetString(t)
		// The first target is no longer special and is treated like any other.
		// We retain the logic to set sync on quit to true by default for it, as a convenience.
		if i == 0 {
			yes := true
			target.SyncOnQuit = &yes
		}
		cfg.SyncTargets = append(cfg.SyncTargets, target)
	}

	if fs.NArg() > 0 && fs.Arg(0) == "clean" {
		cfg.RunClean = true
	}

	return cfg, nil
}

func parseTargetString(raw string) SyncTarget {
	target := SyncTarget{Original: raw}
	parts := strings.Split(raw, "|")
	pathPart := parts[0]

	remoteParts := strings.SplitN(pathPart, ":", 2)

	// This is the updated logic. It now checks that the remote name is longer than one character,
	// which correctly excludes Windows drive letters like "C:".
	if len(remoteParts) == 2 && remoteParts[0] != "" && !strings.Contains(remoteParts[0], "\\") && len(remoteParts[0]) > 1 {
		target.Type = Gdrive
		target.RemoteName = remoteParts[0]
		target.Path = remoteParts[1]
	} else {
		target.Type = Local
		target.Path = pathPart
	}

	if len(parts) > 1 && parts[1] != "" {
		intervalSec, err := strconv.Atoi(parts[1])
		if err == nil {
			target.Interval = time.Duration(intervalSec) * time.Second
		}
	} else {
		// Interval of 0 means live sync (watcher mode). A negative interval means it's the primary target.
		// We set it to 0 and let the launcher logic handle the primary target case.
		target.Interval = 0
	}

	if len(parts) > 2 && parts[2] != "" {
		syncOnQuit, err := strconv.ParseBool(parts[2])
		if err == nil {
			target.SyncOnQuit = &syncOnQuit
		}
	}

	return target
}
