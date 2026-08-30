#!/bin/sh
# Install stamp from a published GitHub release.
#
# The release workflow publishes one archive per platform plus a checksums file,
# and `stamp self-update` refuses to replace the binary unless the checksum
# matches. This script holds itself to the same standard: it is the first thing
# that ever puts stamp on a machine, so it must not be the weaker path.
#
#   curl -fsSL https://raw.githubusercontent.com/p-arndt/stamp/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version 0.2.0 --bin-dir ~/.local/bin
#
# POSIX sh only: this runs under dash, busybox ash and macOS's sh, so there are
# no arrays, no [[ ]], no `local` and no pipefail anywhere below.

set -eu

OWNER="p-arndt"
REPO="stamp"
BINARY="stamp"
RELEASES_URL="https://github.com/${OWNER}/${REPO}/releases"
API_URL="https://api.github.com/repos/${OWNER}/${REPO}"

# Everything the user reads goes to stderr, so stdout stays clean for the one
# thing a caller might want to capture: the version of what was installed.
say() { printf '%s\n' "$*" >&2; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'USAGE'
stamp installer: download a released stamp binary and verify its checksum.

Usage:
  install.sh [--version <v>] [--bin-dir <path>]

Options:
  --version <v>    version to install, with or without a leading "v".
                   Default "latest", which resolves the newest release.
  --bin-dir <path> directory to install into. Default "$HOME/.local/bin".
  --help           print this and exit.

Environment (the flags win):
  STAMP_VERSION    same as --version
  STAMP_BIN_DIR    same as --bin-dir
  GITHUB_TOKEN     sent as a bearer token on GitHub API calls only, for
                   machines behind a rate-limited address.
USAGE
}

VERSION="${STAMP_VERSION:-latest}"
BIN_DIR="${STAMP_BIN_DIR:-}"

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || fail "--version needs a value, e.g. --version 0.2.0"
		VERSION="$2"
		shift 2
		;;
	--bin-dir)
		[ $# -ge 2 ] || fail "--bin-dir needs a value, e.g. --bin-dir \$HOME/.local/bin"
		BIN_DIR="$2"
		shift 2
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		usage >&2
		fail "unknown option $1"
		;;
	esac
done

# The default is deliberately a user-writable directory: an installer that needs
# sudo to do its job is an installer people run as root out of habit.
[ -n "$BIN_DIR" ] || BIN_DIR="$HOME/.local/bin"

# --- platform -----------------------------------------------------------------

detect_platform() {
	uname_s="$(uname -s)"
	uname_m="$(uname -m)"

	case "$uname_s" in
	Linux) OS="linux" ;;
	Darwin) OS="darwin" ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		say "this is a POSIX shell installer; on Windows use the PowerShell one:"
		say "  irm https://raw.githubusercontent.com/${OWNER}/${REPO}/main/install.ps1 | iex"
		fail "unsupported operating system \"$uname_s\""
		;;
	*) OS="" ;;
	esac

	case "$uname_m" in
	x86_64 | amd64) ARCH="amd64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	*) ARCH="" ;;
	esac

	if [ -z "$OS" ] || [ -z "$ARCH" ]; then
		say "unsupported platform: uname -s said \"$uname_s\", uname -m said \"$uname_m\""
		say "stamp is published for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64,"
		say "windows/amd64 and windows/arm64."
		fail "download a binary by hand from ${RELEASES_URL} instead"
	fi
}

# --- prerequisites ------------------------------------------------------------

has() { command -v "$1" >/dev/null 2>&1; }

# Everything is checked before anything is downloaded: failing halfway through
# leaves the user wondering whether something was already installed.
check_tools() {
	if has curl; then
		DOWNLOADER="curl"
	elif has wget; then
		DOWNLOADER="wget"
	else
		fail "neither curl nor wget is installed; install one of them and run this again"
	fi

	has tar || fail "tar is not installed; install it and run this again"

	if has sha256sum; then
		SHA_TOOL="sha256sum"
	elif has shasum; then
		SHA_TOOL="shasum"
	else
		fail "no SHA-256 tool found; install sha256sum (coreutils) or shasum and run this again"
	fi

	has mktemp || fail "mktemp is not installed; install it and run this again"
}

sha256_of() {
	case "$SHA_TOOL" in
	sha256sum) sha256sum "$1" | cut -d' ' -f1 ;;
	shasum) shasum -a 256 "$1" | cut -d' ' -f1 ;;
	esac
}

# --- downloading --------------------------------------------------------------

# fetch_to writes a URL to a file. The -f / --server-response handling matters:
# without it a 404 HTML page is written to the file and only surfaces later as an
# unreadable archive.
fetch_to() {
	fetch_url="$1"
	fetch_dest="$2"
	if [ "$DOWNLOADER" = "curl" ]; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$fetch_dest" "$fetch_url"
	else
		wget -q --https-only -O "$fetch_dest" "$fetch_url"
	fi
}

# fetch_api reads a GitHub API response to stdout. GITHUB_TOKEN is only ever
# sent here, never to the release download host, so a token cannot leak to a
# redirect target outside the API.
fetch_api() {
	api_url="$1"
	if [ "$DOWNLOADER" = "curl" ]; then
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			curl -fsSL --proto '=https' --tlsv1.2 \
				-H "Authorization: Bearer ${GITHUB_TOKEN}" \
				-H "Accept: application/vnd.github+json" "$api_url"
		else
			curl -fsSL --proto '=https' --tlsv1.2 \
				-H "Accept: application/vnd.github+json" "$api_url"
		fi
	else
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			wget -q --https-only \
				--header="Authorization: Bearer ${GITHUB_TOKEN}" \
				--header="Accept: application/vnd.github+json" -O- "$api_url"
		else
			wget -q --https-only \
				--header="Accept: application/vnd.github+json" -O- "$api_url"
		fi
	fi
}

# resolve_version turns "latest" into a concrete version and strips a leading v.
#
# The tag is pulled out with sed rather than jq, which is not installed on a
# machine that has not installed anything yet. Prereleases are flagged as such on
# GitHub, so releases/latest already skips them and no filtering is needed here.
resolve_version() {
	if [ "$VERSION" != "latest" ]; then
		VERSION="${VERSION#v}"
		return
	fi

	say "Resolving the latest release…"
	body="$(fetch_api "${API_URL}/releases/latest")" ||
		fail "could not reach the GitHub API; check the network, or pass --version <v> to skip the lookup"

	# One field per line first, so the greedy .* cannot run past the tag_name
	# field into some later string that happens to contain a quote.
	tag="$(printf '%s\n' "$body" | tr ',' '\n' |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"

	[ -n "$tag" ] ||
		fail "no tag_name in the response from ${API_URL}/releases/latest; pass --version <v> to install a known version, or check ${RELEASES_URL}"

	VERSION="${tag#v}"
}

# --- install ------------------------------------------------------------------

main() {
	detect_platform
	check_tools
	resolve_version

	archive="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
	checksums="${BINARY}_${VERSION}_checksums.txt"
	base="${RELEASES_URL}/download/v${VERSION}"

	TMP_DIR="$(mktemp -d)"
	# The trap covers the failure paths too, including the checksum mismatch:
	# a rejected archive must not be left lying around for someone to run.
	trap 'rm -rf "$TMP_DIR"' EXIT
	trap 'rm -rf "$TMP_DIR"; exit 130' INT
	trap 'rm -rf "$TMP_DIR"; exit 143' TERM

	say "Installing ${BINARY} ${VERSION} for ${OS}/${ARCH}…"

	fetch_to "${base}/${archive}" "${TMP_DIR}/${archive}" ||
		fail "could not download ${base}/${archive}; check that version v${VERSION} exists at ${RELEASES_URL}"
	fetch_to "${base}/${checksums}" "${TMP_DIR}/${checksums}" ||
		fail "could not download the checksums file ${base}/${checksums}; refusing to install an unverified binary"

	# The checksums file covers every archive of the release, so `sha256sum -c`
	# on the whole file would fail on the platforms that were not downloaded.
	# The one expected line is picked out by filename and compared as a string.
	expected="$(awk -v name="$archive" '$2 == name { print $1 }' "${TMP_DIR}/${checksums}" | head -n 1)"
	[ -n "$expected" ] ||
		fail "${checksums} has no entry for ${archive}; refusing to install an unverified binary"

	actual="$(sha256_of "${TMP_DIR}/${archive}")"
	if [ "$actual" != "$expected" ]; then
		say "checksum mismatch for ${archive}"
		say "  expected ${expected}"
		say "  actual   ${actual}"
		fail "the download does not match the published checksum; nothing was installed"
	fi
	say "Checksum verified."

	tar -xzf "${TMP_DIR}/${archive}" -C "$TMP_DIR" ||
		fail "could not extract ${archive}"
	[ -f "${TMP_DIR}/${BINARY}" ] ||
		fail "${archive} does not contain a ${BINARY} binary; report this at https://github.com/${OWNER}/${REPO}/issues"

	mkdir -p "$BIN_DIR" || fail "could not create ${BIN_DIR}; choose another with --bin-dir"
	[ -w "$BIN_DIR" ] || fail "${BIN_DIR} is not writable; choose another with --bin-dir, or run this with the rights to write there"

	# Staged inside the target directory and moved into place, so the rename is
	# atomic on the same filesystem: an interrupted install never leaves a
	# truncated binary on PATH, and replacing a stamp that is currently running
	# works (the old inode stays alive until the process exits).
	staged="${BIN_DIR}/.${BINARY}.install.$$"
	# A leftover staging file from a killed run would otherwise block this one.
	rm -f "$staged"
	cp "${TMP_DIR}/${BINARY}" "$staged" || fail "could not write to ${BIN_DIR}"
	chmod 0755 "$staged"
	mv -f "$staged" "${BIN_DIR}/${BINARY}" || {
		rm -f "$staged"
		fail "could not move the binary into ${BIN_DIR}"
	}

	say "Installed ${BIN_DIR}/${BINARY}"

	case ":${PATH}:" in
	*":${BIN_DIR}:"*) ;;
	*)
		# Adding the line is left to the user on purpose: an installer that
		# edits shell rc files is an installer nobody can fully undo.
		say ""
		say "${BIN_DIR} is not on your PATH. Add this to your shell profile"
		say "(~/.zshrc, ~/.bashrc or ~/.profile) and open a new shell:"
		say ""
		say "  export PATH=\"${BIN_DIR}:\$PATH\""
		say ""
		;;
	esac

	# Running the installed binary is the only proof that what landed is what
	# was meant to land: the right architecture, executable, and this version.
	"${BIN_DIR}/${BINARY}" version ||
		fail "${BIN_DIR}/${BINARY} was installed but does not run; check that it matches this machine (${OS}/${ARCH})"
}

main
