package version

import "testing"

func TestResolveBumps(t *testing.T) {
	cases := []struct {
		current, arg, want string
	}{
		{"0.4.0", "minor", "0.5.0"},
		{"0.4.0", "patch", "0.4.1"},
		{"0.4.0", "major", "1.0.0"},
		{"1.2.3", "1.4.0", "1.4.0"},
		{"1.2.3", "2.0.0-beta.1", "2.0.0-beta.1"},
		// A bump off a pre-release drops the pre-release: 1.0.0-rc.1 -> patch
		// is 1.0.0, the release the candidate was for.
		{"1.0.0-rc.1", "patch", "1.0.0"},
		{"1.0.0-rc.1", "minor", "1.1.0"},
	}
	for _, c := range cases {
		got, err := Resolve(c.current, c.arg)
		if err != nil {
			t.Errorf("Resolve(%q, %q): %v", c.current, c.arg, err)
			continue
		}
		if got != c.want {
			t.Errorf("Resolve(%q, %q) = %q, want %q", c.current, c.arg, got, c.want)
		}
	}
}

func TestResolveRejects(t *testing.T) {
	for _, arg := range []string{"", "v1.2.3", "1.2", "nonsense", "1.2.3.4"} {
		if got, err := Resolve("0.4.0", arg); err == nil {
			t.Errorf("Resolve(%q) = %q, want an error", arg, got)
		}
	}
}

func TestParseRejectsVPrefix(t *testing.T) {
	// The v belongs to the tag, never to the stored version.
	if _, err := Parse("v1.0.0"); err == nil {
		t.Fatal("Parse(\"v1.0.0\") should fail")
	}
}

func TestCompare(t *testing.T) {
	if equal, err := Compare("0.4.0", "0.5.0"); err != nil || equal {
		t.Errorf("0.4.0 -> 0.5.0: equal=%v err=%v", equal, err)
	}
	if equal, err := Compare("0.5.0", "0.5.0"); err != nil || !equal {
		t.Errorf("0.5.0 -> 0.5.0: equal=%v err=%v, want equal", equal, err)
	}
	if _, err := Compare("0.5.0", "0.4.0"); err == nil {
		t.Error("going backwards should fail")
	}
	// A pre-release sorts below its release.
	if _, err := Compare("1.0.0", "1.0.0-beta.1"); err == nil {
		t.Error("1.0.0 -> 1.0.0-beta.1 should fail")
	}
	if equal, err := Compare("1.0.0-beta.1", "1.0.0"); err != nil || equal {
		t.Errorf("1.0.0-beta.1 -> 1.0.0: equal=%v err=%v", equal, err)
	}
}

func TestResolvePre(t *testing.T) {
	cases := []struct {
		current, kind, id, want string
	}{
		// A fresh series starts at .1 on the bumped base.
		{"1.2.3", "patch", "beta", "1.2.4-beta.1"},
		{"1.2.3", "minor", "beta", "1.3.0-beta.1"},
		{"1.2.3", "major", "beta", "2.0.0-beta.1"},
		{"1.2.3", "patch", "", "1.2.4-beta.1"}, // the default identifier
		// A bump always bumps, off a pre-release too: the pre-release is
		// stripped and the keyword applied to what is left. These are the same
		// numbers npm's prepatch/preminor/premajor and uv's --bump produce.
		{"1.3.0-beta.1", "patch", "beta", "1.3.1-beta.1"},
		{"1.3.0-beta.1", "minor", "beta", "1.4.0-beta.1"},
		{"1.3.0-beta.1", "major", "beta", "2.0.0-beta.1"},
		// And it does not depend on which digits happen to be zero.
		{"1.3.1-beta.1", "minor", "beta", "1.4.0-beta.1"},
		{"1.3.0-beta.9", "patch", "beta", "1.3.1-beta.1"},
		{"2.0.0-beta.1", "major", "beta", "3.0.0-beta.1"},
		// The bare form is the one that walks the running series.
		{"1.3.0-beta.1", "", "beta", "1.3.0-beta.2"},
		{"1.3.0-beta.9", "", "beta", "1.3.0-beta.10"},
		{"1.2.4-rc.7", "", "rc", "1.2.4-rc.8"},
		// A new identifier restarts the counter on the same base.
		{"1.3.0-beta.2", "", "rc", "1.3.0-rc.1"},
		// A pre-release without a counter is treated as .0.
		{"1.3.0-beta", "", "beta", "1.3.0-beta.1"},
	}
	for _, c := range cases {
		got, err := ResolvePre(c.current, c.kind, c.id)
		if err != nil {
			t.Errorf("ResolvePre(%q, %q, %q): %v", c.current, c.kind, c.id, err)
			continue
		}
		if got != c.want {
			t.Errorf("ResolvePre(%q, %q, %q) = %q, want %q", c.current, c.kind, c.id, got, c.want)
		}
	}
}

func TestResolvePreRejects(t *testing.T) {
	// An explicit version is `stamp release`'s job, and an identifier has to be
	// a legal semver pre-release.
	for _, c := range []struct{ kind, id string }{
		{"1.3.0", "beta"},
		{"", "beta"}, // 1.2.3 is stable, so there is no series to continue
		{"nonsense", "beta"},
		{"patch", "beta 1"},
		{"patch", "beta_1"},
		{"patch", "beta."},
	} {
		if got, err := ResolvePre("1.2.3", c.kind, c.id); err == nil {
			t.Errorf("ResolvePre(1.2.3, %q, %q) = %q, want an error", c.kind, c.id, got)
		}
	}
}

// Going from a later identifier back to an earlier one is downwards in semver
// and must be caught by the ordinary version check, not silently tagged.
func TestPreSeriesOrder(t *testing.T) {
	if _, err := Compare("1.3.0-rc.1", "1.3.0-beta.1"); err == nil {
		t.Error("rc.1 -> beta.1 should fail")
	}
	if _, err := Compare("1.3.0-beta.1", "1.3.0-rc.1"); err != nil {
		t.Errorf("beta.1 -> rc.1: %v", err)
	}
}

func TestResolveFinal(t *testing.T) {
	got, err := Resolve("1.3.0-rc.2", Final)
	if err != nil || got != "1.3.0" {
		t.Errorf("Resolve(1.3.0-rc.2, final) = %q, %v; want 1.3.0", got, err)
	}
	// Nothing to promote from a stable version.
	if _, err := Resolve("1.3.0", Final); err == nil {
		t.Error("final on a stable version should fail")
	}
}

func TestContinuesSeries(t *testing.T) {
	cases := []struct {
		current, kind string
		want          bool
	}{
		{"1.3.0-rc.1", "", true}, // a bare `stamp prerelease` continues it
		{"1.2.4-beta.1", "", true},
		{"1.2.3", "", false}, // no series to continue
		// Any bump opens a series for a different release, so it takes the
		// configured identifier rather than inheriting this one.
		{"1.3.0-rc.1", "patch", false},
		{"1.3.0-rc.1", "minor", false},
		{"1.3.0-rc.1", "major", false},
		{"nonsense", "", false},
	}
	for _, c := range cases {
		if got := ContinuesSeries(c.current, c.kind); got != c.want {
			t.Errorf("ContinuesSeries(%q, %q) = %v, want %v", c.current, c.kind, got, c.want)
		}
	}
}

func TestPreIDOf(t *testing.T) {
	for in, want := range map[string]string{
		"1.3.0-rc.2":      "rc",
		"1.3.0-beta":      "beta",
		"1.3.0-alpha.1.2": "alpha.1",
		"1.3.0":           "",
		"nonsense":        "",
	} {
		if got := PreIDOf(in); got != want {
			t.Errorf("PreIDOf(%q) = %q, want %q", in, got, want)
		}
	}
}
