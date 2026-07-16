# Changelog

## v0.7.1 (2026-07-16)

### Fixes
- resolve symlinked scan roots before walking so tracked paths remain repository-relative
- match Git's global configuration precedence for exclusion files
- fail closed when included Git configuration prevents reliable ignore resolution
- suppress cleanup candidates whenever repository or ignore state is unknown

## v0.7.0 (2026-07-16)

### Features
- scan repositories and ordinary directories without requiring an installed Git executable
- inspect repository history across all refs and preserve unknown Git state conservatively
- create backups and perform filesystem cleanup actions with native Go implementations

### Fixes
- honor repository, global, system, and default Git exclusion files during cleanup classification
- preserve scan-root paths when go-git canonicalizes macOS filesystem symlinks
- upgrade go-git and its dependency chain to remove reachable known vulnerabilities

### Other
- require Go 1.25 or newer
- add local build, install, test, vet, and run recipes

## v0.6.0 (2026-05-29)

### Features
- path-traversal safety and tests for apply command generation

### Fixes
- terminate tar options before backup paths

### Other
- extract git file set handling, parallelize tests, and dedupe elapsed timing
- update internal implementation
