package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const (
	defaultChunk   = 4096
	defaultRenames = 3
)

var sem chan struct{}

func init() {
	n := runtime.NumCPU()
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	sem = make(chan struct{}, n)
}

func randomName(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func scrubName(path string, passes int) (string, error) {
	dir := filepath.Dir(path)
	cur := path
	for range passes {
		name, err := randomName(16 + mrand.IntN(64))
		if err != nil {
			return cur, err
		}
		next := filepath.Join(dir, name)
		if err := os.Rename(cur, next); err != nil {
			return cur, err
		}
		if d, err := os.Open(dir); err == nil {
			d.Sync()
			d.Close()
		}
		cur = next
	}
	return cur, nil
}

func shredChunk(f *os.File, offset int64, size int) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, iv)

	plain := make([]byte, size)
	if _, err := f.ReadAt(plain, offset); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	cipherText := make([]byte, size)
	stream.XORKeyStream(cipherText, plain)

	if _, err := f.WriteAt(cipherText, offset); err != nil {
		return err
	}
	return f.Sync()
}

func shredFile(path string) error {
	chunk := defaultChunk
	renames := defaultRenames
	if chunk <= 0 {
		return errors.New("chunk size must be > 0")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size > 0 {
		rounds := (size + int64(chunk) - 1) / int64(chunk)
		for i := range rounds {
			offset := i * int64(chunk)
			n := int64(chunk)
			if offset+n > size {
				n = size - offset
			}
			if err := shredChunk(f, offset, int(n)); err != nil {
				return err
			}
			fmt.Printf("[%s] round %d/%d offset=%d len=%d\n", path, i+1, rounds, offset, n)
		}
	}

	if err := f.Truncate(0); err != nil {
		return err
	}
	if err := fullSync(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	final, err := scrubName(path, renames)
	if err != nil {
		return err
	}
	if final != path {
		fmt.Printf("[%s] renamed -> %s\n", path, filepath.Base(final))
	}
	return os.Remove(final)
}

func shredDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	record := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	for _, e := range entries {
		child := filepath.Join(path, e.Name())
		info, err := os.Lstat(child)
		if err != nil {
			record(err)
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if err := os.Remove(child); err != nil {
				record(err)
			} else {
				fmt.Printf("%s: symlink removed\n", child)
			}
		case info.IsDir():
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				record(shredDir(p))
			}(child)
		default:
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if err := shredFile(p); err != nil {
					record(err)
					return
				}
				fmt.Printf("%s: shredded\n", p)
			}(child)
		}
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	final, err := scrubName(path, defaultRenames)
	if err != nil {
		return err
	}
	if final != path {
		fmt.Printf("renamed dir -> %s\n", filepath.Base(final))
	}
	return os.Remove(final)
}

func shred(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return os.Remove(path)
	case mode.IsDir():
		return shredDir(path)
	case mode.IsRegular():
		return shredFile(path)
	default:
		return fmt.Errorf("refusing non-regular file (%s)", mode)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <path> [path...]\n", os.Args[0])
		os.Exit(2)
	}

	fmt.Fprintln(os.Stderr, "note: on APFS/btrfs/ZFS or SSDs, original blocks may persist (best-effort shred)")

	var wg sync.WaitGroup
	var mu sync.Mutex
	exit := 0
	for _, p := range os.Args[1:] {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if err := shred(p); err != nil {
				mu.Lock()
				fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
				exit = 1
				mu.Unlock()
				return
			}
			fmt.Printf("%s: done\n", p)
		}(p)
	}
	wg.Wait()
	os.Exit(exit)
}
