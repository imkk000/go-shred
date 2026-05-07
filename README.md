# go-shred

A drop-in replacement for `rm` that **moves files to trash by default** and **shreds + permanently deletes when `-f` is given**. Recursive, parallel, with a docker-pull-style live progress UI.

Supported platforms: **Linux** and **macOS (Apple Silicon)**. The build tags refuse to compile on anything else.

## Usage

```sh
go build
./go-shred [flags] <path|glob>...
```

| Command | Effect |
|---|---|
| `go-shred file.txt` | move to trash (recoverable) |
| `go-shred -f file.txt` | shred contents, then delete permanently |
| `go-shred -rf '*.bak'` | shred all matching files (`f` triggers force) |
| `go-shred --force dir/` | shred recursively |
| `go-shred --empty-trash` | shred everything currently in the trash |
| `go-shred -- -weird-name` | trash a file literally named `-weird-name` |
| `go-shred missing-path` | silent, exit 0 (`rm -f` semantics) |

Globs (`*`, `?`, `[…]`) are expanded by go-shred itself when the shell hasn't already expanded them (e.g. quoted patterns or invocations from scripts).

Any short flag containing `f` (`-f`, `-rf`, `-fr`, `-rfv`, …) and the long `--force` enable shred mode. All other flags are silently ignored, so existing `rm`-style muscle memory works.

## Trash location

| OS | Path | Sidecars |
|---|---|---|
| Linux | `$XDG_DATA_HOME/Trash/files/` (default `~/.local/share/Trash/files/`) | `…/info/<name>.trashinfo` per [XDG Trash spec](https://specifications.freedesktop.org/trash-spec/trashspec-1.0.html) |
| macOS | `~/.Trash/` | none — Finder-native |

Trash mode is a single `os.Rename`. Cross-filesystem moves (e.g. trashing something on a USB drive into the home trash) fail with:

```
cross-filesystem trash not supported; use -f to force-delete
```

…rather than silently degrading to a copy.

## How `-f` (shred) works

For each regular file:

1. `chmod 0600` if the file isn't already writable, then open `O_RDWR`.
2. Walk the file in 4 KiB chunks. For each chunk: generate a fresh AES-256 key + IV via `crypto/rand`, XOR the plaintext with the AES-CTR keystream, write back at the same offset, `fsync`.
3. `Truncate(0)` to drop the size from the inode/directory entry.
4. `fullSync` — `fcntl(F_FULLFSYNC)` on macOS, plain `fsync` on Linux.
5. Rename to a random hex name 3 times, fsyncing the parent directory between renames.
6. `unlink`.

Directories are processed post-order (children first) and rmdir'd after all contents are gone. Symlinks are unlinked, never followed. Non-regular nodes (sockets, fifos, devices) found inside a target directory are unlinked, not opened for shredding.

## Concurrency

- Top-level paths run in parallel goroutines.
- Walking is sequential per top-level path — no goroutine explosion on deep trees.
- File shredding is bounded by a global semaphore sized `clamp(NumCPU, 2, 8)`. Disk I/O parallelism flattens out fast; more workers just adds queue contention.

## Progress UI

When stderr is a TTY, one live-updating line per top-level path:

```
file.txt                         [=============>----------------]  512.0KB/1.0MB  50%
project-dir                      [=============================>]  done
broken.bin                       [------------------------------]  FAIL: permission denied
notes.bak                        [=============================>]  trashed
```

The renderer redraws the block every 80ms via ANSI cursor-up; new jobs appear as soon as their goroutines schedule. When stderr isn't a TTY (pipes, scripts), one final line per job is printed after everything finishes.

## Why these choices

- **AES-CTR with a fresh random key per chunk.** The keystream from a one-shot random key is statistically indistinguishable from random — equivalent to writing `/dev/urandom` over the file. Using a cipher just makes "throw away the key" the explicit mental model.
- **Multiple scrub renames before unlink.** Some filesystems retain the directory entry's most recent name in metadata or journals; renaming through several random names reduces what a forensic tool can recover about the original filename.
- **Truncate before unlink.** Without this, the inode/dir entry still records the original file size, leaking that "a file of size N existed."
- **`F_FULLFSYNC` on macOS.** Apple's `fsync(2)` only flushes to the drive's volatile write cache. `fcntl(F_FULLFSYNC)` is the only way to force a flush to non-volatile storage. Without it, shredded writes can sit in the SSD cache when the program exits.
- **Symlinks are unlinked, not followed.** Otherwise, shredding a link would silently destroy the target — a footgun.
- **Concurrency capped at 8.** Disk I/O parallelism flattens out quickly; more workers mostly trade throughput for queue contention.

## Caveats — read this if you actually care about "unrecoverable"

`-f` is **best-effort**. On these setups, in-place overwrite does **not** destroy the original physical blocks:

- **Copy-on-write filesystems** (APFS, btrfs, ZFS) — writes go to new blocks; originals remain until garbage-collected.
- **SSDs / flash with wear-leveling** — the FTL remaps logical blocks to wherever it pleases; "overwriting" logical block 42 doesn't touch the cells that held it before.
- **Encrypted volumes / journaled filesystems** — may retain old data in journal regions or unallocated extents.
- **Hardlinked files** — overwriting via one path destroys the data the other links point to too, but only the named link is removed.

If your threat model needs reliable destruction, the only durable answers are full-disk encryption from day one (so "shredding" reduces to forgetting the key) or physical destruction of the media.

## Files

| File | Purpose |
|---|---|
| `main.go` | flag parsing, glob expansion, walk, parallel orchestration |
| `progress.go` | multi-line live renderer (`Job`, `Progress`, ANSI redraw) |
| `trash_linux.go` | XDG trash + `.trashinfo` sidecars + orphan cleanup |
| `trash_darwin.go` | `~/.Trash/` (Finder-native) |
| `fsync_linux.go` | plain `fsync` |
| `fsync_darwin.go` | `fcntl(F_FULLFSYNC)` |
