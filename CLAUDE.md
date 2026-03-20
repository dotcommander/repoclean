# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

`repoclean` is a Go CLI tool that scans git repositories for cleanup opportunities. It walks a directory tree, enriches files with git metadata (tracked/ignored/stale/orphaned status), classifies content, detects duplicates, and categorizes files into actionable buckets: delete, archive, untrack, move, or rename.

## Build & Test

```bash
go build .                          # build binary
go test ./... -count=1              # all tests (integration test builds the binary internally)
go test ./internal/cleanup/         # unit tests only (classify, score, duplicates)
go test -run TestIntegrationCleanupScanner  # integration test only
```

No external dependencies — stdlib only (`go.mod` has zero requires).

## Usage

```bash
./repoclean --path /some/repo           # JSON output (default)
./repoclean --path /some/repo --report  # human-readable cleanup plan
./repoclean --path /some/repo --exec    # shell commands for copy-paste
./repoclean --path /some/repo --apply   # dry-run (shows what would execute)
./repoclean --path /some/repo --apply --confirm  # actually execute with backup
```

Flags: `--max-depth` (default 5), `--stale-days` (default 90).

## Architecture

Single-package CLI (`main.go`) + one internal package (`internal/cleanup/`).

### Pipeline (sequential)

1. **Walk** (`walker.go`) — `filepath.WalkDir` with depth limit, skips `.git` and nested repos, marks empty dirs. Calls `markTracked` which shells out to `git ls-files` to set `Tracked`/`Ignored` on each file.
2. **Enrich** (`git.go`) — Adds git intelligence: staleness (days since last commit touching file) and orphan detection (file was deleted in git but still exists on disk).
3. **Classify** (`classify.go`) — Reads first 512 bytes of text files concurrently, sets `ContentClass`: generated, scratch/todo, log dump, config, or meaningful. Uses worker pool sized to `runtime.NumCPU()`.
4. **FindDuplicates** (`duplicates.go`) — Two signals: backup suffix patterns (`-v2`, `-old`, `.backup`, etc.) and basename+size collisions (excludes common names like `main.go`).
5. **Categorize** (`main.go:categorize()`) — The core decision engine. Priority-ordered classification into 9 buckets using pattern maps and heuristics defined as package-level vars in `main.go`.

### Key Types (`types.go`)

- `FileInfo` — internal enriched file record (all fields populated by walk/enrich/classify)
- `FileCandidate` — JSON output per file with reason, score, hints
- `ScanResult` — top-level output with 9 category slices + summary map
- `ContentClass` — enum for content-aware classification

### Output Modes

- **JSON** (default) — `ScanResult` marshaled with `json.MarshalIndent`
- **Report** (`--report`) — tabular human-readable plan grouped by category
- **Exec** (`--exec`) — shell commands with proper quoting
- **Apply** (`--apply`) — dry-run showing commands; `--apply --confirm` executes with tar backup of all affected files

### Categorization Priority (in `categorize()`)

Files are classified in this order — first match wins:

1. Skip: live `~/go/bin/` symlink targets, dotfiles (unless whitelisted), nested repo files
2. Ignored files: selective cleanup (dev docs, backup files, temp files at root, archive files)
3. Migration files (`*/migrations/*`) → always clean
4. Data files (`.db`, `.sqlite`, `.csv`, `.sql`, `.bak`) → always archive, never delete
5. Scaffold remnants (`public/vite.svg`, etc.) → delete
6. System/temp files (`.DS_Store`, `.tmp`, `.log`) → delete
7. Broken symlinks → report
8. Large files (>10MB) → manual review
9. Dev artifacts (tracked files matching `looper*`/`flow*` prefixes or `*_SUMMARY.md`/`*-spec.md` suffixes) → untrack + archive
10. Misplaced docs (non-allowlisted `.md` at repo root) → move to `docs/`
11. Doc renames (uppercase/underscore filenames in `docs/`) → kebab-case
12. Misplaced scripts (`.sh` at repo root) → move to `scripts/` or archive
13. Untrack candidates (tracked binaries, `.env`, build output, generated content) → `git rm --cached`
14. Remaining untracked files → archive

### Scoring (`score.go`)

0-100 confidence score combining: untracked (+20), staleness (+15/25/35), size (+10/20), content class (+10/15/20), orphaned (+25), duplicate (+20), empty (+30). Capped at 100.
