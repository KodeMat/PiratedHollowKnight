// /internal/util/util.go
package util

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// GetDirLastModTime finds the most recent modification time of any file in a directory tree.
func GetDirLastModTime(dirPath string) (time.Time, error) {
	var latestModTime time.Time
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.ModTime().After(latestModTime) {
				latestModTime = info.ModTime()
			}
		}
		return nil
	})
	// If the directory doesn't exist, WalkDir returns an error. We treat this
	// as a zero time, which is not a fatal error for our logic.
	if err != nil && !os.IsNotExist(err) {
		return time.Time{}, err
	}
	return latestModTime, nil
}

// (Rest of file is unchanged)
func PathExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
func CopyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := CopyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
