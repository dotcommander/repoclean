# repoclean — build & development tasks
# Usage: `just` lists recipes; `just <recipe>` runs one.

# Binary name (main package lives at repo root, so build target is ".")
bin := "repoclean"

# Install dir: GOBIN if set, else GOPATH/bin
gobin := `gobin="$(go env GOBIN)"; [ -n "$gobin" ] && echo "$gobin" || echo "$(go env GOPATH)/bin"`

# List available recipes (default)
default:
    @just --list

# Build the binary into the repo root
build:
    go build -o {{bin}} .

# Install: build, then symlink the in-repo binary into GOBIN.
# Symlink (not `go install`) so `just build` updates the installed copy instantly.
install: build
    @mkdir -p "{{gobin}}"
    @ln -sf "{{justfile_directory()}}/{{bin}}" "{{gobin}}/{{bin}}"
    @echo "installed {{bin}} -> {{gobin}}/{{bin}}"

# Run tests
test:
    go test ./...

# Vet
vet:
    go vet ./...

# Build and run with trailing args (e.g. `just run --path /some/repo --report`)
run *args:
    go run . {{args}}

# Remove the built binary
clean:
    rm -f {{bin}}
