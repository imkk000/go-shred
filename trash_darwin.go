//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func trashRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".Trash"), nil
}

func trashFilesDir() (string, error) {
	return trashRoot()
}

func uniqueTrashTarget(dir, base string) string {
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s %d%s", stem, i+1, ext)
		}
		target := filepath.Join(dir, name)
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			return target
		}
	}
}

func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	root, err := trashRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	target := uniqueTrashTarget(root, filepath.Base(abs))
	if err := os.Rename(abs, target); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("cross-filesystem trash not supported; use -f to force-delete")
		}
		return err
	}
	return nil
}

func cleanupOrphanInfo() {}
