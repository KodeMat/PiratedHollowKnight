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

// optionalInt is a custom flag type that can be treated as a boolean (if no value)
// or an integer (if a value is provided). A value of -1 represents "infinite".
type optionalInt int

func (i *optionalInt) String() string {
	if *i == -1 {
		return "infinite"
	}
	return strconv.Itoa(int(*i))
}

func (i *optionalInt) Set(value string) error {
	// This is the case where the flag is present but has no value, e.g., "--download-retries"
	if value == "true" {
		*i = -1 // Use -1 as the sentinel for infinite
		return nil
	}
	// This is the case where a value is provided, e.g., "--download-retries=5"
	val, err := strconv.Atoi(value)
	if err != nil {
		return err
	}
	*i = optionalInt(val)
	return nil
}

// IsBoolFlag makes the flag parser treat this as a boolean flag
// if it's present on the command line without an explicit value.
func (i *optionalInt) IsBoolFlag() bool {
	return true
}

// Config holds all application settings loaded from flags.
type Config struct {
	HollowKnightInstallPath string
	SyncTargets             []SyncTarget
	SyncOnQuit              bool
	DownloadRetries         optionalInt
	RcloneConfigPath        string
	ForceRcloneAuth         bool
	LogLevel                string
	RunClean                bool
}

// SyncType defines whether a target is local or on Google Drive.
type SyncType int

const (
	Local SyncType = iota
	Gdrive
)

// SyncTarget holds the parsed information for a single save target.
type SyncTarget struct {
	Type       SyncType
	Path       string
	RemoteName string
	Interval   time.Duration
	SyncOnQuit *bool
	Original   string
}

// stringSlice is a custom flag type for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// Load parses command-line flags and arguments to build the application configuration.
func Load() (*Config, error) {
	fs := flag.NewFlagSet("main", flag.ExitOnError)

	cfg := &Config{}
	var targets stringSlice
	var installPath string

	// Set a default value for DownloadRetries before parsing.
	cfg.DownloadRetries = 1

	fs.Var(&targets, "target", "Master/backup save location. Repeatable. Format: \"path|interval|quit_sync\"")
	fs.BoolVar(&cfg.SyncOnQuit, "sync-on-quit", false, "Globally enable sync on game exit for targets without a 'quit' option.")
	fs.StringVar(&installPath, "install-path", "", "Path to the Hollow Knight game installation directory. Defaults to user's Documents/Hollow Knight.")
	fs.Var(&cfg.DownloadRetries, "download-retries", "Number of times to retry download. If flag is present without a value, retries are infinite.")
	fs.StringVar(&cfg.RcloneConfigPath, "config-path", "", "Path to the rclone.conf file. Defaults to 'rclone.conf' in the executable's directory.")
	fs.BoolVar(&cfg.ForceRcloneAuth, "auth", false, "Force the rclone authentication wizard to run for online targets.")
	fs.StringVar(&cfg.LogLevel, "log-level", "quiet", "Set logging verbosity. Options: info, warn, error, quiet.")

	fs.Parse(os.Args[1:])

	if installPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("could not determine user home directory: %w", err)
		}
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
		if i == 0 {
			target.Interval = -1
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

	if remoteParts := strings.SplitN(pathPart, ":", 2); len(remoteParts) == 2 && remoteParts[0] != "" && len(remoteParts[0]) < len(pathPart) && !strings.Contains(remoteParts[0], "\\") {
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
