# stamp - task runner
#
# Requires just >= 1.39 (for the `read()` function used to read VERSION).
#
# Layout:
#   main.go              - the `stamp` CLI entry point   (-> stamp / stamp.exe)
#   internal/config      - .stamp.yml, components, plus auto-detection
#   internal/source      - version locations: text file, JSON, YAML, TOML fields
#   internal/version     - semver resolution and bumping
#   internal/gitx        - the git commands stamp needs
#   internal/release     - preflight, write, commit, tag, push
#   internal/ui          - terminal output and the confirmation prompt
#   (self-update and the update notice come from github.com/p-arndt/selfupdate)
#   install.sh           - one-liner installer for macOS and Linux (curl … | sh)
#   install.ps1          - one-liner installer for Windows        (irm … | iex)
#   VERSION              - single source of truth (stamped into the binary)
#
# Portability: recipe bodies are plain command invocations with no shell syntax,
# so the same line runs under both `sh` and PowerShell. Where a task genuinely
# needs shell logic it is split into `[unix]` and `[windows]` recipes of the
# same name, and just picks the right one per platform.

set windows-shell := ["pwsh.exe", "-NoLogo", "-NoProfile", "-Command"]

# Static, libc-free binaries: the same thing the release workflow ships.
export CGO_ENABLED := "0"

BIN := if os_family() == "windows" { "stamp.exe" } else { "stamp" }

_MODULE := "github.com/p-arndt/stamp/internal/buildinfo"
_VERSION := trim(read("VERSION"))
_COMMIT := `git rev-parse --short HEAD`
_DATE := datetime_utc('%Y-%m-%dT%H:%M:%SZ')

_LDFLAGS := "-s -w" + \
    " -X " + _MODULE + ".Version=" + _VERSION + \
    " -X " + _MODULE + ".Commit=" + _COMMIT + \
    " -X " + _MODULE + ".Date=" + _DATE

# Default: show the recipe list.
default:
    @just --list

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Build a plain dev binary -> stamp; version reports as "dev".
build:
    go build -o {{BIN}} .

# Build a stripped, static release binary stamped with the current VERSION.
build-release:
    go build -trimpath -ldflags "{{_LDFLAGS}}" -o {{BIN}} .

# Install the release binary into ~/.local/bin (unix) so `stamp` is on PATH.
[unix]
install: build-release
    mkdir -p ~/.local/bin
    install -m 0755 {{BIN}} ~/.local/bin/stamp

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

# Run vet, the full test suite and the installer syntax checks. What CI runs.
check: vet test check-installers

vet:
    go vet ./...

test:
    go test ./...

# Tests with verbose output, useful when an integration test fails.
test-v:
    go test -v ./...

test-race:
    go test -race ./...

fmt:
    gofmt -w .

# Parse both install scripts without running them.
[unix]
check-installers:
    #!/usr/bin/env sh
    # The installers are served straight from the default branch to people
    # piping them into a shell, so a syntax error is published the moment it is
    # pushed, and this recipe is the gate before that happens. shellcheck and pwsh
    # are used when present and skipped when not; the parse checks themselves
    # are what must always run.
    set -e
    sh -n install.sh
    if command -v shellcheck >/dev/null 2>&1; then
        shellcheck install.sh
    else
        echo "shellcheck not found, install.sh only checked with sh -n"
    fi
    if command -v pwsh >/dev/null 2>&1; then
        pwsh -NoLogo -NoProfile -Command '$e = $null; [System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path install.ps1), [ref]$null, [ref]$e); if ($e) { $e; exit 1 }'
    else
        echo "pwsh not found, install.ps1 not parsed"
    fi

[windows]
check-installers:
    $e = $null; [System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path install.ps1), [ref]$null, [ref]$e); if ($e) { $e; exit 1 }
    if (Get-Command sh -ErrorAction SilentlyContinue) { sh -n install.sh } else { Write-Host "sh not found - install.sh not checked" }

[unix]
fmt-check:
    #!/usr/bin/env sh
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
        echo "not gofmt-clean:"
        echo "$unformatted"
        exit 1
    fi

# ---------------------------------------------------------------------------
# Release
# ---------------------------------------------------------------------------

# Print the current version.
version:
    @echo {{_VERSION}}

# Cut a release with stamp itself. `just release minor` -> 0.1.0 becomes 0.2.0.
# Uses the freshly built binary rather than an installed one, so a release is
# always cut by the code being released.
release bump="patch": build-release
    ./{{BIN}} release {{bump}}

# Show what a release would do, changing nothing.
release-dry bump="patch": build-release
    ./{{BIN}} release {{bump}} --dry-run

# ---------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------

# Re-record assets/demo.gif from demo/stamp.tape. Needs `vhs`
# (brew install vhs). Nothing real is recorded: demo/setup.sh builds a throwaway
# repository with a bare remote under $TMPDIR, so the release in the GIF is a real
# release of an invented project.
[unix]
demo: build
    DEMO_SETUP="$PWD/demo/setup.sh" PATH="$PWD:$PATH" vhs demo/stamp.tape

# ---------------------------------------------------------------------------
# Housekeeping
# ---------------------------------------------------------------------------

[unix]
clean:
    rm -rf {{BIN}} dist

[windows]
clean:
    if (Test-Path {{BIN}}) { Remove-Item -Force {{BIN}} }
    if (Test-Path dist) { Remove-Item -Recurse -Force dist }
