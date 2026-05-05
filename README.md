# go-shred

A best-effort file/directory shredder. Overwrites file contents with cryptographically random bytes in chunks, scrub-renames the path several times, then unlinks. Walks directories recursively and processes files concurrently.

## Usage

```sh
go build
./go-shred <path> [path...]
```

Each path may be a regular file, a directory (recursive), or a symlink (the link itself is removed; the target is **not** followed).

## How it works

For each regular file:

1. Open `O_RDWR` and stat the size.
2. Walk the file in 4 KiB chunks. For each chunk:
   - Generate a fresh AES-256 key + IV with `crypto/rand`.
   - Read the plaintext, XOR it with the AES-CTR keystream, write the ciphertext back at the same offset.
   - `fsync` so the OS commits the page.
3. `Truncate(0)` to drop the file size from the inode/directory entry.
4. `fullSync` — on macOS this is `fcntl(F_FULLFSYNC)`, on Linux it's plain `fsync`.
5. Rename the file to a random hex name 3 times (each rename is followed by a directory `fsync`).
6. `unlink`.

For directories, children are processed first (post-order), then the directory itself is scrub-renamed and removed. Files within a directory are shredded concurrently, gated by a semaphore sized `clamp(NumCPU, 2, 8)`. Subdirectories recurse in their own goroutines without holding a worker slot, so deep trees don't deadlock.

## Why these choices

- **AES-CTR with a fresh random key per chunk.** The keystream from a one-shot random key is statistically indistinguishable from random — equivalent to writing `/dev/urandom` over the file. Using a cipher just makes "throw away the key" the explicit mental model. (A simpler `rand.Read(buf); WriteAt(buf, off)` would be equivalent.)
- **Multiple scrub renames before unlink.** Some filesystems retain the directory entry's most recent name in metadata or journals; renaming through several random names reduces what a forensic tool can recover about the original filename.
- **Truncate before unlink.** Without this, the inode/dir entry still records the original file size, leaking that "a file of size N existed."
- **`F_FULLFSYNC` on macOS.** Apple's `fsync(2)` only flushes to the drive's volatile write cache. `fcntl(F_FULLFSYNC)` is the only way to force a flush to non-volatile storage. Without it, "shred" on macOS could leave writes sitting in the SSD cache when the program exits.
- **Symlinks are unlinked, not followed.** Otherwise, shredding a link would silently destroy the target — a footgun.
- **Special files (sockets, fifos, devices) are refused.** `O_RDWR` on a device or fifo would do something surprising and probably destructive.
- **Concurrency capped at 8.** Disk I/O parallelism flattens out quickly; more workers mostly trade throughput for queue contention.

## Caveats

This is **best-effort**. On the following systems, in-place overwrite does **not** destroy the original physical blocks:

- **Copy-on-write filesystems** (APFS on macOS, btrfs, ZFS) — writes go to new blocks; the originals stay until garbage-collected.
- **SSDs / flash with wear-leveling** — the FTL maps logical blocks to whichever physical cells it likes; "overwriting" logical block 42 doesn't touch the cells that previously held it.
- **Encrypted volumes / journaled filesystems** — may retain old data in journal regions or unallocated extents.

For real assurance on these, the only reliable option is full-disk encryption from day one (so "shredding" reduces to forgetting the key) or physical destruction.

## Files

- `main.go` — entry point, traversal, chunk overwrite, scrub-rename.
- `fsync_darwin.go` — `F_FULLFSYNC` implementation for macOS.
- `fsync_other.go` — plain `fsync` for everything else.
