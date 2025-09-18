// /internal/installer/installer.go
package installer

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"pirated-hollow-knight/internal/backup"
	"pirated-hollow-knight/internal/config"
	"pirated-hollow-knight/internal/log"
	"pirated-hollow-knight/internal/util"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/schollz/progressbar/v3"
)

const (
	expectedSHA1      = "edf6dbde9a65a6304e096b61b0b2226a6e8a2416"
	rcloneDownloadURL = "https://downloads.rclone.org/rclone-current-windows-amd64.zip"
)

type Extractor struct {
	Path string
	Type string
}

func EnsureDependencies(cfg *config.Config) error {
	log.Log.Info("--- Checking Dependencies ---")
	if err := ensureHollowKnightInstalled(cfg); err != nil {
		return err
	}
	if err := ensureRcloneInstalled(cfg); err != nil {
		return err
	}
	log.Log.Info("--- All dependencies are satisfied ---")
	return nil
}

func ensureHollowKnightInstalled(cfg *config.Config) error {
	if util.PathExists(cfg.HollowKnightInstallPath) {
		log.Log.Info("✅ Hollow Knight installation found at: %s", cfg.HollowKnightInstallPath)
		return nil
	}
	log.Log.Warn("Hollow Knight installation not found. Starting download process...")
	if err := downloadAndExtractHollowKnight(cfg); err != nil {
		return fmt.Errorf("failed to install Hollow Knight: %w", err)
	}
	log.Log.Info("✅ Hollow Knight installed successfully.")
	return nil
}

func downloadAndExtractHollowKnight(cfg *config.Config) error {
	tempDownloadDir, _ := os.MkdirTemp("", "hk-download-*")
	defer os.RemoveAll(tempDownloadDir)
	tempExtractDir, _ := os.MkdirTemp("", "hk-extract-*")
	defer os.RemoveAll(tempExtractDir)

	finalURL, err := getFinalURLFromHTMX("https://buzzheavier.com/ibozyrc7vpjq/download")
	if err != nil {
		return err
	}
	filename, err := getDirectDownloadInfo(finalURL)
	if err != nil {
		return err
	}
	downloadedFilePath := filepath.Join(tempDownloadDir, filename)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Log.Warn("\n🚨 Interrupt received during download. Cleaning up...")
		os.Remove(downloadedFilePath)
		os.Exit(1)
	}()
	defer signal.Stop(c)

	var lastErr error
	isInfinite := cfg.DownloadRetries == -1

	// This loop handles both finite and infinite retries.
	for i := 1; ; i++ {
		if isInfinite {
			log.Log.Info("Download attempt %d (retrying indefinitely)...", i)
		} else {
			totalAttempts := int(cfg.DownloadRetries) + 1
			if i > totalAttempts {
				break
			}
			log.Log.Info("Download attempt %d of %d...", i, totalAttempts)
		}

		// Perform download and verification
		if err := downloadFileWithProgress(finalURL, downloadedFilePath); err != nil {
			lastErr = err
			log.Log.Warn("Attempt failed (download): %v", err)
			_ = os.Remove(downloadedFilePath) // Clean up partial file
		} else if err := verifySHA1(downloadedFilePath, expectedSHA1); err != nil {
			lastErr = err
			log.Log.Warn("Attempt failed (verification): %v", err)
			_ = os.Remove(downloadedFilePath)
		} else {
			// Success!
			lastErr = nil
			break
		}

		// If infinite, wait before retrying to avoid hammering the server.
		if isInfinite {
			time.Sleep(5 * time.Second)
		}
	}

	if lastErr != nil {
		return fmt.Errorf("all download attempts failed. Last error: %w", lastErr)
	}

	extractor, _ := findExtractor()
	var cmd *exec.Cmd
	if extractor.Type == "winrar" {
		cmd = exec.Command(extractor.Path, "x", downloadedFilePath, tempExtractDir)
	} else {
		cmd = exec.Command(extractor.Path, "x", downloadedFilePath, fmt.Sprintf("-o%s", tempExtractDir))
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extraction failed: %w\n%s", err, string(output))
	}

	entries, _ := os.ReadDir(tempExtractDir)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "Hollow Knight v") {
			oldPath := filepath.Join(tempExtractDir, entry.Name())
			if err := os.Rename(oldPath, cfg.HollowKnightInstallPath); err != nil {
				return err
			}
			log.Log.Info("✅ Game installed to %s", cfg.HollowKnightInstallPath)
			return nil
		}
	}
	return fmt.Errorf("could not find game folder in archive")
}

// --- Rest of installer.go remains unchanged ---
func ensureRcloneInstalled(cfg *config.Config) error {
	gdriveTargets := getGdriveTargets(cfg)
	if len(gdriveTargets) == 0 {
		log.Log.Info("No GDrive targets specified, skipping rclone check.")
		return nil
	}
	log.Log.Info("GDrive target(s) found, checking rclone setup...")
	if _, err := exec.LookPath("rclone"); err != nil {
		exePath, _ := os.Executable()
		localRclonePath := filepath.Join(filepath.Dir(exePath), "rclone.exe")
		if !util.PathExists(localRclonePath) {
			log.Log.Warn("rclone.exe not found. Starting automatic download...")
			if err := downloadAndExtractRclone(localRclonePath); err != nil {
				return fmt.Errorf("failed to automatically install rclone: %w", err)
			}
			log.Log.Info("✅ rclone.exe installed successfully.")
		}
	} else {
		log.Log.Info("✅ rclone found in PATH.")
	}
	if cfg.ForceRcloneAuth {
		log.Log.Warn("`--auth` flag detected. Forcing rclone configuration wizard...")
		return backup.RunRcloneConfigWizard(cfg)
	}
	if !util.PathExists(cfg.RcloneConfigPath) {
		log.Log.Warn("Rclone config not found at '%s'. Starting one-time setup...", cfg.RcloneConfigPath)
		return backup.RunRcloneConfigWizard(cfg)
	}
	remotes, err := backup.GetConfiguredRemotes(cfg)
	if err != nil {
		return fmt.Errorf("could not verify rclone configuration: %w", err)
	}
	allRemotesFound := true
	for _, target := range gdriveTargets {
		if _, found := remotes[target.RemoteName]; !found {
			log.Log.Warn("Remote '%s' is specified in a target but not found in the config file.", target.RemoteName)
			allRemotesFound = false
		}
	}
	if !allRemotesFound {
		log.Log.Warn("One or more required remotes are missing. Starting configuration wizard...")
		return backup.RunRcloneConfigWizard(cfg)
	}
	log.Log.Info("✅ Rclone configuration verified.")
	return nil
}

func getGdriveTargets(cfg *config.Config) []config.SyncTarget {
	var gdriveTargets []config.SyncTarget
	for _, t := range cfg.SyncTargets {
		if t.Type == config.Gdrive {
			gdriveTargets = append(gdriveTargets, t)
		}
	}
	return gdriveTargets
}

func downloadAndExtractRclone(destPath string) error {
	log.Log.Info("Downloading rclone from %s...", rcloneDownloadURL)
	resp, err := http.Get(rcloneDownloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	log.Log.Info("Download complete. Extracting rclone.exe...")
	zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	for _, file := range zipReader.File {
		if strings.HasSuffix(file.Name, "rclone.exe") {
			rc, _ := file.Open()
			defer rc.Close()
			outFile, _ := os.Create(destPath)
			defer outFile.Close()
			io.Copy(outFile, rc)
			log.Log.Info("Successfully extracted rclone.exe to %s", destPath)
			return nil
		}
	}
	return fmt.Errorf("could not find rclone.exe in archive")
}

func findExtractor() (*Extractor, error) {
	if runtime.GOOS == "windows" {
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		winrarPaths := []string{filepath.Join(programFiles, "WinRAR", "WinRAR.exe"), filepath.Join(programFilesX86, "WinRAR", "WinRAR.exe")}
		for _, path := range winrarPaths {
			if util.PathExists(path) {
				return &Extractor{Path: path, Type: "winrar"}, nil
			}
		}
		sevenZipPaths := []string{filepath.Join(programFiles, "7-Zip", "7z.exe"), filepath.Join(programFilesX86, "7-Zip", "7z.exe")}
		for _, path := range sevenZipPaths {
			if util.PathExists(path) {
				return &Extractor{Path: path, Type: "7z"}, nil
			}
		}
	}
	if path, err := exec.LookPath("7z"); err == nil {
		return &Extractor{Path: path, Type: "7z"}, nil
	}
	return nil, fmt.Errorf("no supported extractor found (WinRAR or 7-Zip)")
}

func verifySHA1(filePath, expectedHash string) error {
	log.Log.Info("Verifying SHA-1 hash for %s...", filepath.Base(filePath))
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha1.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	calculatedHash := hex.EncodeToString(hasher.Sum(nil))
	if calculatedHash != expectedHash {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, calculatedHash)
	}
	log.Log.Info("✅ SHA-1 hash verification successful.")
	return nil
}

func downloadFileWithProgress(url, destPath string) error {
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}
	f, _ := os.Create(destPath)
	defer f.Close()
	bar := progressbar.DefaultBytes(resp.ContentLength, filepath.Base(destPath))
	io.Copy(io.MultiWriter(f, bar), resp.Body)
	return nil
}

func getFinalURLFromHTMX(htmxURL string) (string, error) {
	log.Log.Info("Simulating htmx request to get redirect URL...")
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest("GET", htmxURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create htmx request: %w", err)
	}
	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-encoding", "gzip, deflate, br, zstd")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("hx-current-url", "https://buzzheavier.com/ibozyrc7vpjq")
	req.Header.Set("hx-request", "true")
	req.Header.Set("priority", "u=1, i")
	req.Header.Set("referer", "https://buzzheavier.com/ibozyrc7vpjq")
	req.Header.Set("sec-ch-ua", `"Opera GX";v="121", "Chromium";v="137", "Not/A)Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36 OPR/121.0.0.0")
	req.Host = "buzzheavier.com"
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("htmx request failed: %w", err)
	}
	defer resp.Body.Close()
	redirectURL := resp.Header.Get("HX-Redirect")
	if redirectURL == "" {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server response did not contain HX-Redirect header. Status: %s, Body: %s", resp.Status, string(bodyBytes))
	}
	log.Log.Info("✅ Successfully found HX-Redirect header: %s", redirectURL)
	return redirectURL, nil
}

func getDirectDownloadInfo(finalURL string) (string, error) {
	resp, err := http.Head(finalURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	disposition := resp.Header.Get("Content-Disposition")
	if disposition != "" {
		_, params, err := mime.ParseMediaType(disposition)
		if err == nil {
			if f, ok := params["filename"]; ok {
				return f, nil
			}
		}
	}
	if parsedURL, err := url.Parse(finalURL); err == nil {
		if base := filepath.Base(parsedURL.Path); base != "." && base != "/" {
			return base, nil
		}
	}
	return "", fmt.Errorf("could not determine filename")
}
