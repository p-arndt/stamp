# stamp - task runner
#
# Shared recipes (build, test, fmt, ci, clean, ...) live in .just/, copied from
# ~/coding/just-common. Edit them there and run `just sync-common`; this file
# only holds what is specific to stamp.
#
# release.just is deliberately not imported: it runs `stamp` from PATH, and this
# repository releases itself with its own freshly built binary instead, so a
# release is always cut by the code being released.
#
# Requires just >= 1.39 (for the `read()` function used to read VERSION).

import '.just/common.just'
import '.just/go.just'

# `ci` is redefined below to add the installer checks.
set allow-duplicate-recipes

BIN_NAME := "stamp"
BUILDINFO_PKG := "github.com/p-arndt/stamp/internal/buildinfo"

# Install the release binary into ~/.local/bin (unix) so `stamp` is on PATH.
[unix]
install: build-release
    mkdir -p ~/.local/bin
    install -m 0755 {{_BIN}} ~/.local/bin/stamp

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

# Every check CI runs, plus the installer syntax checks.
ci: fmt-check vet test check-installers

# Former name of `ci`.
check: ci

# Tests with verbose output, useful when an integration test fails.
test-v:
    go test -v ./...

test-race:
    go test -race ./...

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
    ./{{_BIN}} release {{bump}}

# Show what a release would do, changing nothing.
release-dry bump="patch": build-release
    ./{{_BIN}} release {{bump}} --dry-run

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
