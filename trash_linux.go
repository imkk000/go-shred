//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func trashRoot() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "Trash"), nil
}

func trashDirs() (filesDir, infoDir string, err error) {
	root, err := trashRoot()
	if err != nil {
		return "", "", err
	}
	filesDir = filepath.Join(root, "files")
	infoDir = filepath.Join(root, "info")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return "", "", err
	}
	return filesDir, infoDir, nil
}

func trashFilesDir() (string, error) {
	root, err := trashRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "files"), nil
}

func uniqueTrashName(filesDir, infoDir, base string) (target, info string) {
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		target = filepath.Join(filesDir, name)
		info = filepath.Join(infoDir, name+".trashinfo")
		_, terr := os.Lstat(target)
		_, ierr := os.Lstat(info)
		if os.IsNotExist(terr) && os.IsNotExist(ierr) {
			return target, info
		}
	}
}

func encodeTrashPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	filesDir, infoDir, err := trashDirs()
	if err != nil {
		return err
	}
	target, info := uniqueTrashName(filesDir, infoDir, filepath.Base(abs))

	body := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		encodeTrashPath(abs),
		time.Now().Format("2006-01-02T15:04:05"))
	if err := os.WriteFile(info, []byte(body), 0o600); err != nil {
		return err
	}
	if err := os.Rename(abs, target); err != nil {
		os.Remove(info)
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("cross-filesystem trash not supported; use -f to force-delete")
		}
		return err
	}
	return nil
}

func cleanupOrphanInfo() {
	root, err := trashRoot()
	if err != nil {
		return
	}
	filesDir := filepath.Join(root, "files")
	infoDir := filepath.Join(root, "info")
	entries, err := os.ReadDir(infoDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".trashinfo") {
			continue
		}
		baseName := strings.TrimSuffix(name, ".trashinfo")
		if _, err := os.Lstat(filepath.Join(filesDir, baseName)); errors.Is(err, fs.ErrNotExist) {
			os.Remove(filepath.Join(infoDir, name))
		}
	}
}
