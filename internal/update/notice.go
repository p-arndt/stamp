package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// checkInterval is how often the passive notice refreshes its cached "latest
// version" — at most once per this window, so running stamp never repeatedly hits
// the network.
const checkInterval = 24 * time.Hour

// noticeTimeout bounds the background refresh so a slow or unreachable GitHub
// can't delay a command by more than this. A release is an interactive
// operation the user is waiting on; the update check never gets to be the slow
// part of it.
const noticeTimeout = 1500 * time.Millisecond

// state is the cached result of the last update check, stored as JSON in
// <UserConfigDir>/stamp/update-check.json.
type state struct {
	LastCheck time.Time `json:"last_check"`
	Latest    string    `json:"latest"` // latest version seen, without a leading "v"
}

// statePath returns the cache file location. It lives in the user's config
// directory rather than the repository: it is a property of the installed
// binary, and nothing about a checked-out project should change because stamp
// looked for an update.
func statePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "stamp", "update-check.json"), nil
}

func loadState() state {
	var st state
	path, err := statePath()
	if err != nil {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st) // a corrupt cache just triggers a fresh check
	// The cached version is printed to the terminal — and into a TUI footer that
	// is itself drawing escape sequences — so it gets the same ingress validation
	// as the release tag it came from: a tampered cache file must not become a
	// terminal-escape injection vector.
	if !validVersion(st.Latest) {
		st.Latest = ""
	}
	return st
}

func saveState(st state) {
	path, err := statePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// disabled reports whether update checking is off for this run: either the user
// set STAMP_NO_UPDATE_CHECK, or this is a source build with no release to compare
// against.
func disabled(current string) bool {
	return os.Getenv("STAMP_NO_UPDATE_CHECK") != "" || isDevVersion(current)
}

// NotifyIfAvailable prints a one-line "newer version available" hint to w when
// the cached latest version is newer than current, then refreshes the cache in
// the background if it's stale. It never blocks longer than noticeTimeout and
// never touches stdout, so machine-readable output (`stamp current`) stays
// clean.
//
// The notice always reflects the *previously* cached check: the refresh here
// updates the cache for the next run. That keeps the hot path free of a
// blocking network call while still converging within a day.
//
// Set STAMP_NO_UPDATE_CHECK to any non-empty value to disable it entirely.
func NotifyIfAvailable(w io.Writer, current string) {
	if disabled(current) {
		return
	}
	st := loadState()

	if st.Latest != "" && IsNewer(st.Latest, current) {
		fmt.Fprintf(w, "\nA newer stamp is available: %s (you have %s). Run `stamp self-update` to upgrade.\n", st.Latest, current)
	}

	if time.Since(st.LastCheck) < checkInterval {
		return
	}
	refresh(NewClient(&http.Client{Timeout: noticeTimeout}), st)
}

// refresh claims the check window immediately (so a hanging network doesn't make
// every run retry), then fetches the latest version bounded by noticeTimeout and
// records it for the next run. It returns the state it settled on.
func refresh(c *Client, st state) state {
	st.LastCheck = time.Now()
	saveState(st) // claim the window up front, even if the fetch below times out

	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()

	done := make(chan string, 1)
	go func() {
		rel, err := c.LatestRelease(ctx)
		if err != nil {
			close(done)
			return
		}
		done <- strings.TrimPrefix(rel.Tag, "v")
	}()

	select {
	case v, ok := <-done:
		if ok && v != "" {
			st.Latest = v
			saveState(st)
		}
	case <-ctx.Done():
		// Timed out; leave the claimed window in place and try again next day.
	}
	return st
}
