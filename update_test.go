package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/p-arndt/selfupdate"
	"github.com/p-arndt/selfupdate/layout"
	"github.com/p-arndt/stamp/internal/ui"
)

// The updater is a library now, so these tests cover the wiring rather than the
// mechanics: that stamp's configuration finds, verifies and installs a release
// shaped the way .github/workflows/release.yml builds one.
//
// Three Config fields are the seams — APIBase points at a loopback server,
// StatePath at a temp cache, ExecutablePath at a throwaway binary — so nothing
// here touches the network or the real installation.

// fakeRelease serves a complete GitHub release, metadata plus assets, and
// returns its base URL.
func fakeRelease(t *testing.T, tag string, assets map[string][]byte) string {
	t.Helper()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := strings.TrimPrefix(r.URL.Path, "/assets/"); name != r.URL.Path {
			body, ok := assets[name]
			if !ok {
				http.Error(w, "no such asset", http.StatusNotFound)
				return
			}
			w.Write(body)
			return
		}
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		out := struct {
			Tag    string  `json:"tag_name"`
			Assets []asset `json:"assets"`
		}{Tag: tag}
		for name := range assets {
			out.Assets = append(out.Assets, asset{Name: name, URL: srv.URL + "/assets/" + name})
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// releaseAssets builds what the release workflow uploads: one archive per
// platform with the binary inside, and a sha256sum-format checksums file.
func releaseAssets(t *testing.T, version string, binary []byte) map[string][]byte {
	t.Helper()

	lay := &layout.Archive{}
	lay.SetDefaults(updateRepo) // what selfupdate.New does with AppName
	archiveName := lay.AssetName(version, runtime.GOOS, runtime.GOARCH)

	archive := tarGz(t, lay.ExecutableName(runtime.GOOS), binary)
	if runtime.GOOS == "windows" {
		archive = zipped(t, lay.ExecutableName(runtime.GOOS), binary)
	}

	sum := sha256.Sum256(archive)
	return map[string][]byte{
		archiveName: archive,
		fmt.Sprintf("stamp_%s_checksums.txt", version): []byte(
			fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)),
	}
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testConfig wires the three seams and returns the stand-in binary's path.
func testConfig(t *testing.T, apiBase string) (selfupdate.Config, string) {
	t.Helper()

	exe := filepath.Join(t.TempDir(), "stamp")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "update-check.json")

	return selfupdate.Config{
		APIBase:        apiBase,
		StatePath:      func() (string, error) { return cache, nil },
		ExecutablePath: func() (string, error) { return exe, nil },
	}, exe
}

// captureUI redirects the ui package's writers into one buffer.
func captureUI(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldErr := ui.Out, ui.Err
	ui.Out, ui.Err = &buf, &buf
	t.Cleanup(func() { ui.Out, ui.Err = oldOut, oldErr })
	return &buf
}

// The whole path: find the release, verify the checksum, swap the binary.
func TestSelfUpdateInstallsNewerRelease(t *testing.T) {
	base := fakeRelease(t, "v1.2.0", releaseAssets(t, "1.2.0", []byte("the new binary")))
	cfg, exe := testConfig(t, base)
	out := captureUI(t)

	if err := runUpdate(context.Background(), cfg, "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "the new binary" {
		t.Errorf("installed binary = %q, want the new binary", got)
	}
	requireContains(t, out.String(), "Updated", "1.0.0", "1.2.0")
}

// check-update reports what it found and writes nothing.
func TestCheckUpdateDoesNotInstall(t *testing.T) {
	base := fakeRelease(t, "v1.2.0", releaseAssets(t, "1.2.0", []byte("the new binary")))
	cfg, exe := testConfig(t, base)
	out := captureUI(t)

	if err := runUpdate(context.Background(), cfg, "1.0.0", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "the old binary" {
		t.Error("check-update wrote to the binary")
	}
	requireContains(t, out.String(), "A newer version is available", "stamp self-update")
}

// Already current: no download, and stamp says so rather than staying silent.
func TestSelfUpdateOnLatestVersion(t *testing.T) {
	base := fakeRelease(t, "v1.2.0", releaseAssets(t, "1.2.0", []byte("the new binary")))
	cfg, exe := testConfig(t, base)
	out := captureUI(t)

	if err := runUpdate(context.Background(), cfg, "1.2.0", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "the old binary" {
		t.Error("the binary was replaced with the same version")
	}
	requireContains(t, out.String(), "you are on the latest version (1.2.0)")
}

// A corrupted download must not be installed. The archive is served intact but
// the checksums file lists a different digest, which is what a tampered release
// looks like from the client side.
func TestSelfUpdateRefusesAChecksumMismatch(t *testing.T) {
	assets := releaseAssets(t, "1.2.0", []byte("the new binary"))
	// The digest of something else entirely, against the archive's real name:
	// the name still resolves, so the run gets as far as comparing hashes.
	lay := &layout.Archive{}
	lay.SetDefaults(updateRepo)
	wrong := sha256.Sum256([]byte("a different binary"))
	assets["stamp_1.2.0_checksums.txt"] = []byte(fmt.Sprintf("%s  %s\n",
		hex.EncodeToString(wrong[:]),
		lay.AssetName("1.2.0", runtime.GOOS, runtime.GOARCH)))

	cfg, exe := testConfig(t, fakeRelease(t, "v1.2.0", assets))
	captureUI(t)

	err := runUpdate(context.Background(), cfg, "1.0.0", false)
	if err == nil {
		t.Fatal("a mismatching checksum was accepted")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("err = %v, want it to name the checksum mismatch", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "the old binary" {
		t.Error("the binary was replaced despite the mismatch")
	}
}

// The asset names the updater looks for have to be the ones the release
// workflow uploads. Nothing else connects the two, so this is the check that
// would catch a rename on either side.
func TestAssetNamesMatchTheReleaseWorkflow(t *testing.T) {
	lay := &layout.Archive{}
	lay.SetDefaults(updateRepo)

	// Mirrors the loop in .github/workflows/release.yml: a per-target archive
	// named stamp_<version>_<goos>_<goarch>, zip on Windows and tar.gz elsewhere.
	want := map[string]string{
		"linux/amd64":   "stamp_1.2.0_linux_amd64.tar.gz",
		"darwin/arm64":  "stamp_1.2.0_darwin_arm64.tar.gz",
		"windows/amd64": "stamp_1.2.0_windows_amd64.zip",
	}
	for target, name := range want {
		goos, goarch, _ := strings.Cut(target, "/")
		if got := lay.AssetName("1.2.0", goos, goarch); got != name {
			t.Errorf("AssetName(%s) = %q, want %q", target, got, name)
		}
	}
	if got := lay.ExecutableName("windows"); got != "stamp.exe" {
		t.Errorf("ExecutableName(windows) = %q, want stamp.exe", got)
	}
}
