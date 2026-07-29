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
