package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	defaultChunk   = 4096
	defaultRenames = 3
)

var sem chan struct{}

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

func openRW(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return nil, err
	}
	if chErr := os.Chmod(path, 0o600); chErr != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR, 0)
}

func shredFile(path string, onProgress func(int64)) error {
	f, err := openRW(path)
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
		chunk := int64(defaultChunk)
		rounds := (size + chunk - 1) / chunk
		for i := int64(0); i < rounds; i++ {
			offset := i * chunk
			n := chunk
			if offset+n > size {
				n = size - offset
			}
			if err := shredChunk(f, offset, int(n)); err != nil {
				return err
			}
			onProgress(n)
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

	final, err := scrubName(path, defaultRenames)
	if err != nil {
		return err
	}
	return os.Remove(final)
}

const (
	itemFile = iota
	itemDir
	itemSymlink
	itemOther
)

type item struct {
	path string
	kind int
	size int64
}

func walk(root string) ([]item, int64, error) {
	var items []item
	var total int64
	var visit func(string) error
	visit = func(p string) error {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		m := info.Mode()
		switch {
		case m&os.ModeSymlink != 0:
			items = append(items, item{p, itemSymlink, 0})
		case m.IsDir():
			entries, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := visit(filepath.Join(p, e.Name())); err != nil {
					return err
				}
			}
			items = append(items, item{p, itemDir, 0})
		case m.IsRegular():
			items = append(items, item{p, itemFile, info.Size()})
			total += info.Size()
		default:
			items = append(items, item{p, itemOther, 0})
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func processOne(root string, prog *Progress, force bool) {
	root = filepath.Clean(root)
	if _, err := os.Lstat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		j := prog.Add(root)
		j.Fail(err)
		return
	}

	job := prog.Add(root)

	if !force {
		if err := moveToTrash(root); err != nil {
			job.Fail(err)
			return
		}
		job.DoneAs("trashed")
		return
	}

	items, total, err := walk(root)
	if err != nil {
		job.Fail(err)
		return
	}
	job.SetTotal(total)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	record := func(err error) {
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	for _, it := range items {
		if it.kind != itemFile {
			continue
		}
		wg.Add(1)
		go func(it item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := shredFile(it.path, job.Add); err != nil {
				record(fmt.Errorf("%s: %w", filepath.Base(it.path), err))
			}
		}(it)
	}
	wg.Wait()

	if firstErr != nil {
		job.Fail(firstErr)
		return
	}

	for _, it := range items {
		switch it.kind {
		case itemSymlink, itemOther:
			if err := os.Remove(it.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				job.Fail(err)
				return
			}
		case itemDir:
			final, err := scrubName(it.path, defaultRenames)
			if err != nil {
				job.Fail(err)
				return
			}
			if err := os.Remove(final); err != nil && !errors.Is(err, fs.ErrNotExist) {
				job.Fail(err)
				return
			}
		}
	}
	job.Done()
}

func hasGlob(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[':
			return true
		case '\\':
			i++
		}
	}
	return false
}

func expandArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if hasGlob(a) {
			matches, _ := filepath.Glob(a)
			out = append(out, matches...)
			continue
		}
		out = append(out, a)
	}
	return out
}

func main() {
	var raw []string
	force := false
	emptyTrash := false
	endOfFlags := false
	for _, a := range os.Args[1:] {
		if !endOfFlags {
			if a == "--" {
				endOfFlags = true
				continue
			}
			if strings.HasPrefix(a, "--") {
				switch a {
				case "--force":
					force = true
				case "--empty-trash":
					emptyTrash = true
				}
				continue
			}
			if len(a) > 1 && a[0] == '-' {
				if strings.ContainsRune(a[1:], 'f') {
					force = true
				}
				continue
			}
		}
		raw = append(raw, a)
	}

	var paths []string
	if emptyTrash {
		force = true
		filesDir, err := trashFilesDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		entries, err := os.ReadDir(filesDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		for _, e := range entries {
			paths = append(paths, filepath.Join(filesDir, e.Name()))
		}
	} else {
		if len(raw) == 0 {
			fmt.Fprintf(os.Stderr, "usage: %s [-f] [--empty-trash] [path|glob]...\n", os.Args[0])
			os.Exit(2)
		}
		paths = expandArgs(raw)
	}
	if len(paths) == 0 {
		os.Exit(0)
	}

	n := max(2, min(8, runtime.NumCPU()))
	sem = make(chan struct{}, n)

	prog := NewProgress(os.Stderr)
	prog.Start()

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			processOne(p, prog, force)
		}(p)
	}
	wg.Wait()
	prog.Stop()
	cleanupOrphanInfo()

	if prog.HasFailures() {
		os.Exit(1)
	}
}
