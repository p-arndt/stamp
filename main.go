// Command stamp cuts a release locally: it resolves the next version, writes it
// into every place the project keeps it, commits, tags and pushes.
//
// Pushing the tag is what triggers the release pipeline. The pipeline never
// decides the version — it validates the tag against the committed version
// (see `stamp verify`) and then builds and publishes. stamp is the release
// controller; CI is the release worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/p-arndt/stamp/internal/buildinfo"
	"github.com/p-arndt/stamp/internal/config"
	"github.com/p-arndt/stamp/internal/gitx"
	"github.com/p-arndt/stamp/internal/release"
	"github.com/p-arndt/stamp/internal/ui"
	"github.com/p-arndt/stamp/internal/update"
	"github.com/p-arndt/stamp/internal/version"
)

const usage = `stamp — cut a release locally: version, commit, tag, push.

Usage:
  stamp release    <patch|minor|major|final|x.y.z>
                                              resolve, write, commit, tag and push
  stamp prerelease [patch|minor|major]        the same, for a pre-release (beta, rc, …)
  stamp set        <patch|minor|major|final|x.y.z>
                                              write the version files only, no git
  stamp current                               print the current version
  stamp verify --tag <tag>                    check a tag against the committed version
  stamp check-update                          report whether a newer stamp is available
  stamp self-update                           replace this binary with the latest release
  stamp version                               print stamp's own version

Flags for release and prerelease:
  --dry-run          show the plan and the checks, change nothing
  --no-push          write, commit and tag locally, do not push
  --no-fetch         skip the network checks (branch up to date, tag free on remote)
  --branch <name>    release from this branch instead of the configured one
  -y, --yes          skip the confirmation prompt
  --type <id>        prerelease only: the identifier, e.g. beta, rc, alpha

A pre-release moves within its series until it is promoted:

  stamp prerelease minor            1.2.3 -> 1.3.0-beta.1
  stamp prerelease                  1.3.0-beta.1 -> 1.3.0-beta.2
  stamp prerelease --type rc        1.3.0-beta.2 -> 1.3.0-rc.1
  stamp prerelease major            1.3.0-rc.1 -> 2.0.0-beta.1
  stamp release final               1.3.0-rc.1 -> 1.3.0

The bump says which release the series is being cut for, so it is only needed
to open a series or to move it to a higher one — a bare "stamp prerelease" cuts
the next candidate of the series already running.

Without --type stamp stays in the series the current version is already in; a
new series takes its identifier from release.prerelease in .stamp.yml, and from
"beta" when that is unset.

Configuration is optional. Without a .stamp.yml, stamp uses a VERSION file (or
package.json), releases from main, tags v<version> and pushes to origin.

Set STAMP_NO_UPDATE_CHECK=1 to silence the passive "newer version" notice.
`

func main() {
	args := os.Args[1:]

	// A previous self-update on Windows leaves the old binary beside the new one
	// (a running .exe can be renamed but not deleted). Sweep it up now that it
	// is no longer running.
	update.CleanupLeftovers()

	if err := run(args); err != nil {
		// Both errQuiet and an aborted release have already printed everything
		// the user needs; anything else gets the standard "error: …" line.
		if !errors.Is(err, errQuiet) && !release.IsAborted(err) {
			ui.Errorf("%v", err)
		}
		os.Exit(1)
	}

	notifyUpdate(args)
}

// notifyUpdate prints the passive "a newer stamp is available" hint after a
// command has done its work. It goes to stderr, so `stamp current` stays usable
// in a shell substitution, and it is suppressed for the update commands
// themselves — they already report the version they found.
func notifyUpdate(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "self-update", "check-update":
			return
		}
	}
	update.NotifyIfAvailable(ui.Err, buildinfo.Version)
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, usage)
		return nil
	}

	switch args[0] {
	case "release":
		return cmdRelease(args[1:], false)
	case "prerelease", "pre":
		return cmdRelease(args[1:], true)
	case "set":
		return cmdSet(args[1:])
	case "current":
		return cmdCurrent(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "self-update":
		return cmdUpdate(false)
	case "check-update":
		return cmdUpdate(true)
	case "version", "--version", "-v":
		fmt.Fprintln(os.Stdout, buildinfo.String())
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(os.Stdout, usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// splitFlags moves flags ahead of positional arguments.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `stamp release minor --dry-run` would otherwise leave --dry-run unparsed and
// treated as a second positional. That form is the natural one to type, so it
// has to work; valueFlags lists the flags that consume the following argument.
func splitFlags(args []string, valueFlags ...string) []string {
	takesValue := make(map[string]bool, len(valueFlags))
	for _, f := range valueFlags {
		takesValue[f] = true
	}

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if takesValue[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

// load opens the repository containing the working directory and reads its
// configuration. Every command needs both.
func load() (*config.Config, *gitx.Repo, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	repo, err := gitx.Open(wd)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return nil, nil, err
	}
	return cfg, repo, nil
}

// cmdRelease backs both `stamp release` and, with pre, `stamp prerelease`.
// The two share every flag, every check and the whole run — they differ only in
// how the bump argument is turned into a version.
func cmdRelease(args []string, pre bool) error {
	name := "release"
	if pre {
		name = "prerelease"
	}
	opts := release.Options{Pre: pre}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show the plan, change nothing")
	fs.BoolVar(&opts.NoPush, "no-push", false, "commit and tag locally, do not push")
	fs.BoolVar(&opts.NoFetch, "no-fetch", false, "skip the network checks")
	fs.StringVar(&opts.Branch, "branch", "", "release from this branch")
	fs.BoolVar(&opts.Yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&opts.Yes, "y", false, "skip the confirmation prompt")
	if pre {
		fs.StringVar(&opts.PreID, "type", "", "pre-release identifier, e.g. beta or rc")
	}
	if err := fs.Parse(splitFlags(args, "branch", "type")); err != nil {
		return err
	}
	// The bump is optional for a pre-release: inside a running series it would
	// carry no information, since all three keywords resolve to the same next
	// candidate. Off a stable version it is required, and version.ResolvePre
	// says so — with the current version in the message, which a usage line
	// could not do.
	maxArgs := 1
	if pre && fs.NArg() == 0 {
		maxArgs = 0
	}
	if fs.NArg() != maxArgs {
		if pre {
			return fmt.Errorf("usage: stamp prerelease [patch|minor|major] [--type <id>] [flags]")
		}
		return fmt.Errorf("usage: stamp release <patch|minor|major|final|x.y.z> [flags]")
	}
	if fs.NArg() == 1 {
		opts.Arg = fs.Arg(0)
	}

	cfg, repo, err := load()
	if err != nil {
		return err
	}

	plan, prepErr := release.Prepare(cfg, repo, opts)
	if plan == nil {
		return prepErr
	}
	plan.Print()
	if prepErr != nil {
		return prepErr
	}

	if !plan.OK() {
		ui.Blank()
		ui.Errorf("preflight failed — nothing was changed")
		return errQuiet
	}

	if opts.DryRun {
		ui.Blank()
		ui.Note("Dry run — nothing was changed.")
		return nil
	}

	if !opts.Yes {
		question := "Proceed with commit, tag and push?"
		if !plan.WillPush() {
			question = "Proceed with commit and tag?"
		}
		ok, err := ui.Confirm(question)
		if err != nil {
			return err
		}
		if !ok {
			ui.Note("Aborted — nothing was changed.")
			return errQuiet
		}
	}

	ui.Blank()
	if err := plan.Run(); err != nil {
		return err
	}
	ui.Blank()
	ui.Step("Done. %s is pushed — the release workflow takes it from here.", plan.Tag)
	return nil
}

func cmdSet(args []string) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: stamp set <patch|minor|major|final|x.y.z>")
	}

	cfg, _, err := load()
	if err != nil {
		return err
	}
	current, err := cfg.Source.Read()
	if err != nil {
		return err
	}
	next, err := version.Resolve(current, fs.Arg(0))
	if err != nil {
		return err
	}
	// `set` is the manual-correction command, so it deliberately does not
	// insist that the new version be higher — fixing a mistyped version means
	// going back.
	if _, err := version.Parse(next); err != nil {
		return err
	}
	for _, src := range cfg.AllSources() {
		if err := src.Write(next); err != nil {
			return fmt.Errorf("writing %s: %w", src.Path(), err)
		}
		ui.Step("%s → %s", src.Describe(), next)
	}
	return nil
}

func cmdCurrent(args []string) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := load()
	if err != nil {
		return err
	}
	current, err := cfg.Source.Read()
	if err != nil {
		return err
	}
	// Bare value on stdout, so `stamp current` can be used in a justfile or a
	// shell substitution.
	fmt.Fprintln(os.Stdout, current)
	return nil
}

// cmdVerify is the CI-side half of stamp: it confirms that the tag being built
// is the tag the committed version would produce.
//
// It compares forwards — it renders the tag template from the committed version
// and compares that to the given tag — rather than trying to strip a prefix off
// the tag. Reversing a template is ambiguous; rendering it is not.
func cmdVerify(args []string) error {
	var tag string
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.StringVar(&tag, "tag", "", "the tag to check, e.g. v0.5.0")
	if err := fs.Parse(splitFlags(args, "tag")); err != nil {
		return err
	}
	if tag == "" && fs.NArg() == 1 {
		tag = fs.Arg(0) // `stamp verify v0.5.0` also works
	}
	if tag == "" {
		return fmt.Errorf("usage: stamp verify --tag <tag>")
	}

	cfg, _, err := load()
	if err != nil {
		return err
	}
	current, err := cfg.Source.Read()
	if err != nil {
		return err
	}
	expected := cfg.RenderTag(current)

	ui.Blank()
	ui.Field("Tag", tag)
	ui.Field("Version", fmt.Sprintf("%s (%s)", current, cfg.Source.Describe()))
	ui.Field("Expected tag", expected)
	ui.Blank()

	mismatch := false
	if expected != tag {
		ui.Fail(fmt.Sprintf("tag %s does not match the committed version %s", tag, current))
		mismatch = true
	} else {
		ui.Pass("tag matches the committed version")
	}

	// Mirrors are verified too: a mirror left behind means the published
	// artifacts would disagree about their own version.
	for _, m := range cfg.Mirrors {
		got, err := m.Read()
		switch {
		case err != nil:
			ui.Fail(fmt.Sprintf("%s: %v", m.Describe(), err))
			mismatch = true
		case got != current:
			ui.Fail(fmt.Sprintf("%s holds %s, expected %s", m.Describe(), got, current))
			mismatch = true
		default:
			ui.Pass(fmt.Sprintf("%s agrees", m.Describe()))
		}
	}

	if mismatch {
		return errQuiet
	}
	return nil
}

// updateTimeout bounds the whole check-download-verify-install cycle. Generous
// compared to the passive notice, since the user explicitly asked for it.
const updateTimeout = 60 * time.Second

// cmdUpdate backs `stamp self-update` and, with checkOnly, `stamp check-update`:
// the first replaces the running binary with the latest release once its
// checksum verifies, the second only reports whether a newer one exists.
//
// Unlike every other command it does not open a repository — updating the
// installed binary has nothing to do with the project the user happens to be
// standing in.
func cmdUpdate(checkOnly bool) error {
	current := buildinfo.Version
	client := update.NewClient(&http.Client{Timeout: updateTimeout})
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	ui.Blank()
	if !checkOnly {
		ui.Field("Current", current)
		ui.Note("Checking for updates…")
	}

	res, err := client.SelfUpdate(ctx, current, checkOnly)
	if err != nil {
		return err
	}

	switch {
	case !update.IsNewer(res.Latest, res.Current):
		ui.Pass(fmt.Sprintf("you are on the latest version (%s)", res.Current))
	case checkOnly:
		ui.Step("A newer version is available: %s", ui.Bump(res.Current, res.Latest))
		ui.Note("Run `stamp self-update` to upgrade.")
	default:
		ui.Blank()
		ui.Step("Updated %s", ui.Bump(res.Current, res.Latest))
		ui.Note("Replaced %s", res.ExePath)
	}
	return nil
}

// errQuiet exits non-zero without an extra error line, for failures that have
// already been printed in their proper form.
var errQuiet = errors.New("failed")
