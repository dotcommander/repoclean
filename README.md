# repoclean

A CLI tool that scans git repositories and tells you what to clean up.

Point it at any repo and it finds junk files, stale artifacts, misplaced docs, tracked files that shouldn't be tracked, and more. It gives you a plan — or runs it for you with a safety backup.

## Install

```bash
go install github.com/dotcommander/repoclean@latest
```

Or build from source:

```bash
git clone https://github.com/dotcommander/repoclean.git
cd repoclean
go build -o repoclean .
```

## Quick Start

```bash
# See what needs cleaning (JSON output)
repoclean --path /path/to/your/repo

# Get a human-readable report
repoclean --path /path/to/your/repo --report

# See the exact shell commands it would run
repoclean --path /path/to/your/repo --exec

# Dry-run: preview what would happen
repoclean --path /path/to/your/repo --apply

# Actually do it (creates a backup first)
repoclean --path /path/to/your/repo --apply --confirm
```

If you skip `--path`, it scans the current directory.

## What It Finds

| Category | What It Catches | What Happens |
|----------|----------------|--------------|
| **Delete** | `.DS_Store`, `.tmp`, `.log`, `Thumbs.db`, empty dirs, `node_modules`, `__pycache__` | Removed |
| **Archive** | Untracked files, `.zip`/`.tar.gz` at repo root, data files (`.db`, `.csv`, `.sql`) | Moved to `.work/archive/` |
| **Untrack** | Tracked `.env` files, compiled binaries, build output (`dist/`, `build/`), `.exe`/`.dll` | `git rm --cached` (keeps file, removes from git) |
| **Dev Artifacts** | Tracked scratch files (`*_SUMMARY.md`, `*-spec.md`, `looper*`, `flow*`) | Untracked + archived |
| **Misplaced Docs** | `.md` files at repo root that aren't README/CHANGELOG/LICENSE/etc. | Moved to `docs/` |
| **Misplaced Scripts** | `.sh` files at repo root | Moved to `scripts/` (if referenced) or archived |
| **Rename Docs** | Uppercase or underscore filenames in `docs/` (e.g. `ARCHITECTURE.md`) | Renamed to kebab-case (`architecture.md`) |
| **Large Files** | Files over 10MB | Flagged for manual review |
| **Broken Links** | Symlinks pointing to missing targets | Flagged for manual review |

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--path` | `.` | Directory to scan |
| `--max-depth` | `5` | How deep to scan into subdirectories |
| `--stale-days` | `90` | Days since last git commit before a file is considered stale |
| `--report` | off | Human-readable output instead of JSON |
| `--exec` | off | Print shell commands you can copy-paste |
| `--apply` | off | Dry-run mode — shows what would happen |
| `--confirm` | off | Used with `--apply` to actually execute (creates tar backup first) |

## Output Formats

### JSON (default)

Returns a `ScanResult` object with arrays for each category (`delete_candidates`, `archive_candidates`, etc.) plus a `summary` with counts. Useful for piping into other tools.

### Report (`--report`)

Prints a table grouped by action type, showing file names, reasons, sizes, and scores.

### Exec (`--exec`)

Outputs copy-paste-ready shell commands grouped by category. Review them, then run what you want.

### Apply (`--apply`)

Interactive mode. Without `--confirm`, shows a dry-run. With `--confirm`, it:

1. Creates a tar.gz backup of every file it will touch
2. Executes each command and reports success/failure
3. Prints a summary at the end

The backup goes to `.work/archive/pre-cleanup-YYYYMMDD-HHMMSS.tar.gz`, so you can always undo.

## How It Works

1. **Walk** — Scans the directory tree (respects `--max-depth`, skips `.git` and nested repos)
2. **Enrich** — Uses `git log` to calculate staleness and detect orphaned files
3. **Classify** — Reads the first 512 bytes of text files to determine content type (generated, scratch, log dump, config, or meaningful)
4. **Detect Duplicates** — Finds backup copies (`*-v2`, `*-old`, `*.backup`) and same-name-same-size files
5. **Categorize** — Applies priority-ordered rules to sort every file into an action bucket
6. **Score** — Each file gets a 0-100 cleanup confidence score based on staleness, size, content type, and other signals

## Safety

- **`--apply` is a dry-run by default.** You must add `--confirm` to execute.
- **Automatic backups.** Before any destructive action, all affected files are tarred up.
- **Data files are never deleted.** `.db`, `.sqlite`, `.csv`, `.sql`, and `.bak` files are always archived, never removed.
- **Live binaries are protected.** If `~/go/bin/` has a symlink pointing to a file in the repo, that file is untouched.
- **Nested repos are skipped.** Directories with their own `.git` are excluded from scanning.

## Requirements

- Go 1.22+ (built with Go 1.26)
- Git (uses `git ls-files` and `git log` for enrichment)

## License

MIT
