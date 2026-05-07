# Notes for Claude

## What this is

A custom `rm` replacement. Default = trash. `-f` / any short flag containing `f` / `--force` = shred + permanent delete. Used as a daily tool on the user's machine.

**Destructive code.** Verify changes carefully — bugs lose data unrecoverably.

## Platforms

Only **Linux** and **macOS (arm64)** are supported. Build tags enforce this — Windows/BSD will fail compilation. Don't try to make it cross-platform unless asked.

## Build & test

```sh
go build ./...
go vet ./...
GOOS=linux  GOARCH=amd64 go build -o /dev/null .
GOOS=linux  GOARCH=arm64 go build -o /dev/null .
GOOS=darwin GOARCH=arm64 go build -o /dev/null .
```

There are no tests yet. Smoke-test against fixtures in `/home/kk/...` (NOT `/tmp` — it's tmpfs and triggers the cross-filesystem EXDEV path on trash mode).

## Layout

| File | Build tag | Role |
|---|---|---|
| `main.go` | — | flag parse, glob, walk, orchestration |
| `progress.go` | — | multi-line live progress (`Job`, `Progress`) |
| `trash_linux.go` | `linux` | XDG trash + `.trashinfo` |
| `trash_darwin.go` | `darwin` | `~/.Trash/` |
| `fsync_linux.go` | `linux` | plain `fsync` |
| `fsync_darwin.go` | `darwin` | `F_FULLFSYNC` |

Each platform pair must export the same symbols (`fullSync`, `moveToTrash`, `trashFilesDir`, `cleanupOrphanInfo`).

## Flow

1. Parse args. Any short flag with `f` or `--force` sets `force=true`. `--empty-trash` enumerates trash files dir and forces `force=true`. `--` ends flag parsing. Other flags are silently ignored (this is intentional — `rm -rf foo` should "just work").
2. Glob-expand args containing `* ? [`.
3. Per top-level path, spawn a goroutine running `processOne(path, prog, force)`.
4. If `!force`: single `os.Rename` to trash. Cross-filesystem returns a friendly EXDEV error with "use -f" hint.
5. If `force`: walk to collect items + total bytes (post-order), shred regular files in parallel via `sem` (cap = `clamp(NumCPU, 2, 8)`), then unlink symlinks/non-regular nodes, then `rmdir` directories.
6. After `prog.Stop()`, always call `cleanupOrphanInfo()` (no-op on darwin).

## Conventions

- **No comments** unless the *why* is non-obvious.
- **No new dependencies.** stdlib only — the user has rejected `urfave/cli` and `golang.org/x/term`.
- Concise, rm-style UX. Silent on missing paths (matches `rm -f`).
- Progress output goes to stderr; nothing else writes to stderr/stdout during a run (would interleave with the renderer).
- Use `Edit` over `Write` when modifying existing files.

## Things to be careful about

- The user explicitly wants `-rf` (and any flag containing `f`) to mean force. Don't add interactive prompts or "are you sure" — they specifically use this *because* `rm` asks too much.
- Don't follow symlinks. Don't open non-regular files (sockets/fifos/devices) with `O_RDWR`.
- The shred path chmods read-only files up to 0600 to be able to open `O_RDWR`. Don't restore the mode — we're deleting it anyway.
- Cross-filesystem trash returns an error rather than falling back to copy. This is intentional. Don't "fix" it.
- The shred-actually-destroys-blocks claim is false on CoW/SSD/encrypted/journaled FS. The README is honest about this — keep it that way.
