// Command stamp cuts a release locally: it resolves the next version, writes it
// into every place the project keeps it, commits, tags and pushes.
//
// Pushing the tag is what triggers the release pipeline. The pipeline never
// decides the version. It validates the tag against the committed version
// (see `stamp verify`) and then builds and publishes. stamp is the release
// controller; CI is the release worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/p-arndt/selfupdate"
	upver "github.com/p-arndt/selfupdate/version"
	"github.com/p-arndt/stamp/internal/buildinfo"
	"github.com/p-arndt/stamp/internal/changelog"
	"github.com/p-arndt/stamp/internal/config"
	"github.com/p-arndt/stamp/internal/gitx"
	"github.com/p-arndt/stamp/internal/release"
	"github.com/p-arndt/stamp/internal/ui"
	"github.com/p-arndt/stamp/internal/version"
)

const usage = `stamp: cut a release locally: version, commit, tag, push.

Usage:
  stamp init                                  write a .stamp.yml describing this repository
  stamp migrate                               rewrite an older .stamp.yml in the current shape
  stamp release    [component] <patch|minor|major|final|x.y.z>
                                              resolve, write, commit, tag and push
  stamp prerelease [component] [patch|minor|major]
                                              the same, for a pre-release (beta, rc, …)
  stamp set        [component] <patch|minor|major|final|x.y.z>
                                              write the version files only, no git
  stamp note       [component] <added|changed|deprecated|removed|fixed|security> <text>
                                              record one user-facing change for the changelog
  stamp changelog  [component]                print the entries noted since the last release
  stamp current    [component]                print the current version
  stamp verify --tag <tag>                    check a tag against the committed version
  stamp check-update                          report whether a newer stamp is available
  stamp self-update                           replace this binary with the latest release
  stamp version                               print stamp's own version

The component is named only in a repository whose .stamp.yml declares one; see
Components below.

Flags for init:
  --dry-run          print the file instead of writing it
  --force            overwrite an existing .stamp.yml
  -y, --yes          take every default instead of asking
  --file <loc>       a version location, repeatable, in "path#field" form,
                     replaces detection, e.g. --file VERSION --file package.json
  --name <name>      project name shown in the output
  --branch <name>    release branch, default the checked-out one
  --remote <name>    remote to push to, default the repository's
  --tag <template>   tag template, default "v{{version}}"
  --commit <template>
                     commit template, default "release: {{tag}}"
  --prerelease <id>  default pre-release identifier, default "beta"

Flags for release and prerelease:
  --dry-run          show the plan and the checks, change nothing
  --no-push          write, commit and tag locally, do not push
  --no-fetch         skip the network checks (branch up to date, tag free on remote)
  --branch <name>    release from this branch instead of the configured one
  --edit             open the rendered changelog section in $EDITOR first
  -y, --yes          skip the confirmation prompt
  --type <id>        prerelease only: the identifier, e.g. beta, rc, alpha

Where the version lives

  version:
    - VERSION                      the first file is the source of truth
    - package.json#version         the rest are written to match it
    - charts/app/Chart.yaml#appVersion
    - pyproject.toml#project.version

  A plain path is a text file holding nothing but the version; "path#field"
  addresses a field inside a JSON, YAML or TOML document, nested fields
  included. The format follows the extension.

Components

  A repository that releases more than one thing declares them, and each is
  versioned and tagged on its own:

    release:                       applies to every component
      branch: main
      tag: "{{component}}-v{{version}}"

    components:
      cli:
        version: [VERSION, package.json#version]
        tag: v{{version}}          overrides just this key
      web:
        version: web/package.json#version

    stamp release web minor

  A component inherits every release key and overrides only the ones it names.
  Without a component name stamp refuses to guess and lists the names instead.

Changelog

  stamp note added "Pre-releases open a beta series"

  writes .stamp/changelog/pre-releases-open-a-beta-series.added.md, a markdown
  file committed with the change it describes. "stamp release" collects the
  fragments, renders them into CHANGELOG.md, deletes them, and puts the same
  section into the annotated tag, all in the release commit. "stamp changelog"
  prints what has piled up so far.

  It is opt-in by use: a repository that has never run "stamp note", has no
  CHANGELOG.md and declares no changelog: block releases exactly as before.

Pre-releases

  stamp prerelease minor            1.2.3 -> 1.3.0-beta.1
  stamp prerelease                  1.3.0-beta.1 -> 1.3.0-beta.2
  stamp prerelease --type rc        1.3.0-beta.2 -> 1.3.0-rc.1
  stamp prerelease minor            1.3.0-rc.1 -> 1.4.0-beta.1
  stamp release final               1.3.0-rc.1 -> 1.3.0

A bump keyword always bumps, off a pre-release as much as off a stable version:
the pre-release is stripped and the keyword applied to what is left. The bare
"stamp prerelease" is the form that walks the series already running, and the
one to put in a script, since repeating it cuts the next candidate rather than
moving the target.

The bare form inherits the identifier of the series it continues, so walking an
rc series does not need --type repeated. A bump opens a series for a different
release and takes its identifier from release.prerelease in .stamp.yml, or from
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
	selfupdate.CleanupLeftovers()

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
// themselves, which already report the version they found.
func notifyUpdate(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "self-update", "check-update":
			return
		}
	}
	// Best effort: a passive hint is never worth failing a command over, so a
	// misconfigured updater simply stays quiet.
	if up, err := newUpdater(selfupdate.Config{}); err == nil {
		up.NotifyIfAvailable(ui.Err, buildinfo.Version)
	}
}

// Repo coordinates for the published releases.
const (
	updateOwner = "p-arndt"
	updateRepo  = "stamp"
)

// newUpdater builds stamp's updater.
//
// Everything except the coordinates and the name of the update command is the
// library's default, and that is deliberate: the defaults describe exactly what
// .github/workflows/release.yml publishes: stamp_<version>_<goos>_<goarch>
// archives with the binary inside, stamp_<version>_checksums.txt beside them,
// and STAMP_NO_UPDATE_CHECK as the opt-out. Overriding any of it here would
// mean the workflow and the updater could drift apart silently.
//
// cfg carries the test seams (APIBase, StatePath, ExecutablePath); production
// passes the zero value.
func newUpdater(cfg selfupdate.Config) (*selfupdate.Updater, error) {
	cfg.Owner = updateOwner
	cfg.Repo = updateRepo
	// The library would default this to "stamp update".
	cfg.UpdateCmd = "stamp self-update"
	return selfupdate.New(cfg)
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, usage)
		return nil
	}

	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "migrate":
		return cmdMigrate(args[1:])
	case "release":
		return cmdRelease(args[1:], false)
	case "prerelease", "pre":
		return cmdRelease(args[1:], true)
	case "set":
		return cmdSet(args[1:])
	case "note":
		return cmdNote(args[1:])
	case "changelog":
		return cmdChangelog(args[1:])
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
// configuration. Every command that touches a version needs both.
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
	if cfg.Legacy {
		// The old shape still loads, so nothing breaks; saying so once on
		// stderr is what eventually gets the file updated.
		ui.Hint("%s still uses version.source / version.mirrors. Run `stamp migrate` to rewrite it as a list", config.FileName)
	}
	return cfg, repo, nil
}

// selectComponent resolves the leading positional argument into the component a
// command is about, and returns the arguments that follow it.
//
// In a repository that declares no components there is nothing to select and
// every argument is passed through. In one that does, the name is required:
// releasing the wrong component is not a mistake stamp is willing to make on
// the user's behalf, and there is no sensible default among equals.
func selectComponent(cfg *config.Config, args []string) (*config.Component, []string, error) {
	if !cfg.Multi {
		return cfg.Only(), args, nil
	}
	if len(args) > 0 {
		if comp := cfg.Lookup(args[0]); comp != nil {
			return comp, args[1:], nil
		}
	}
	return nil, nil, componentRequired(cfg, args, "release")
}

func componentRequired(cfg *config.Config, args []string, verb string) error {
	names := cfg.Names()
	// A bump keyword in the component slot is the ordinary slip: the user
	// typed the command they would type in a single-version repository, so it
	// is not reported as an unknown name.
	got := ""
	if len(args) > 0 && !isBump(args[0]) {
		got = fmt.Sprintf("%q is not one of them; ", args[0])
	}
	return fmt.Errorf("this repository has components (%s): %sname one, e.g. `stamp %s %s minor`",
		strings.Join(names, ", "), got, verb, names[0])
}

// isBump reports whether arg is a bump keyword or an explicit version, i.e.
// something that belongs in the slot after the component name.
func isBump(arg string) bool {
	switch arg {
	case "patch", "minor", "major", "final":
		return true
	}
	_, err := version.Parse(arg)
	return err == nil
}

// cmdRelease backs both `stamp release` and, with pre, `stamp prerelease`.
// The two share every flag, every check and the whole run. They differ only in
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
	fs.BoolVar(&opts.Edit, "edit", false, "open the rendered changelog section in $EDITOR")
	fs.BoolVar(&opts.Yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&opts.Yes, "y", false, "skip the confirmation prompt")
	if pre {
		fs.StringVar(&opts.PreID, "type", "", "pre-release identifier, e.g. beta or rc")
	}
	if err := fs.Parse(splitFlags(args, "branch", "type")); err != nil {
		return err
	}

	cfg, repo, err := load()
	if err != nil {
		return err
	}
	comp, rest, err := selectComponent(cfg, fs.Args())
	if err != nil {
		return err
	}

	// The bump is optional for a pre-release: without one, the run walks the
	// series that is already going. Off a stable version there is no series to
	// walk, so it is required, and version.ResolvePre says so, with the
	// current version in the message, which a usage line could not do.
	switch {
	case len(rest) == 1:
		opts.Arg = rest[0]
	case len(rest) == 0 && pre:
		// A bare pre-release: continue the running series.
	default:
		return fmt.Errorf("usage: %s", releaseUsage(cfg, name, pre))
	}

	plan, prepErr := release.Prepare(cfg, comp, repo, opts)
	if plan == nil {
		return prepErr
	}
	plan.Print()
	if prepErr != nil {
		return prepErr
	}

	if !plan.OK() {
		ui.Blank()
		ui.Errorf("preflight failed, nothing was changed")
		return errQuiet
	}

	if opts.DryRun {
		ui.Blank()
		ui.Note("Dry run. Nothing was changed.")
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
			ui.Note("Aborted. Nothing was changed.")
			return errQuiet
		}
	}

	if opts.Edit {
		if !ui.Interactive() {
			return fmt.Errorf("--edit needs a terminal; drop it, or note the entries with `stamp note` first")
		}
		section, err := editSection(plan.Section)
		if err != nil {
			return err
		}
		plan.Section = section
	}

	ui.Blank()
	if err := plan.Run(); err != nil {
		return err
	}
	ui.Blank()
	ui.Step("Done. %s is pushed. The release workflow takes it from here.", plan.Tag)
	return nil
}

// releaseUsage spells out the argument list this repository actually takes, so
// a monorepo is not told to type a form that would be rejected.
func releaseUsage(cfg *config.Config, name string, pre bool) string {
	slot := "<patch|minor|major|final|x.y.z>"
	if pre {
		slot = "[patch|minor|major]"
	}
	component := ""
	if cfg.Multi {
		component = "<" + strings.Join(cfg.Names(), "|") + "> "
	}
	return fmt.Sprintf("stamp %s %s%s [flags]", name, component, slot)
}

// stringList collects a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// cmdInit writes a .stamp.yml describing the repository as it already is.
//
// The config file is optional, so init is not a prerequisite for anything: it
// exists to turn the detected layout into an editable file, with every default
// written out, so the next thing that happens is a human reading it and
// changing the two lines that are wrong. What it writes is validated by loading
// it back, so a file init produced always parses.
func cmdInit(args []string) error {
	var (
		opts   config.InitOptions
		files  stringList
		dryRun bool
		force  bool
		yes    bool
	)
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.BoolVar(&dryRun, "dry-run", false, "print the file instead of writing it")
	fs.BoolVar(&force, "force", false, "overwrite an existing config file")
	fs.BoolVar(&yes, "yes", false, "take every default instead of asking")
	fs.BoolVar(&yes, "y", false, "take every default instead of asking")
	fs.Var(&files, "file", "a version location, repeatable")
	fs.StringVar(&opts.Name, "name", "", "project name")
	fs.StringVar(&opts.Branch, "branch", "", "release branch")
	fs.StringVar(&opts.Remote, "remote", "", "remote to push to")
	fs.StringVar(&opts.Tag, "tag", "", "tag template")
	fs.StringVar(&opts.Commit, "commit", "", "commit message template")
	fs.StringVar(&opts.PreID, "prerelease", "", "default pre-release identifier")
	if err := fs.Parse(splitFlags(args, "file", "name", "branch", "remote", "tag", "commit", "prerelease")); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: stamp init [flags]")
	}
	opts.Versions = files

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := gitx.Open(wd)
	if err != nil {
		return err
	}
	path := filepath.Join(repo.Root, config.FileName)
	if _, err := os.Stat(path); err == nil && !force && !dryRun {
		ui.Errorf("%s already exists", config.FileName)
		ui.Hint("stamp init --force     overwrite it")
		ui.Hint("stamp init --dry-run   print what init would write")
		return errQuiet
	}

	// The repository knows its own branch and remote better than the defaults
	// do; a repo on master must not get "branch: main" written into its config.
	// Both are best effort: a fresh repository with no commits and no remote
	// still gets a usable file, with the defaults in it.
	if opts.Branch == "" {
		if branch, err := repo.CurrentBranch(); err == nil {
			opts.Branch = branch
		}
	}
	if opts.Remote == "" {
		if remote, err := repo.DefaultRemote(); err == nil {
			opts.Remote = remote
		}
	}

	interactive := ui.Interactive() && !yes && !dryRun
	if interactive {
		if err := askInit(repo.Root, &opts); err != nil {
			return err
		}
	}

	draft, err := config.Init(repo.Root, opts)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Fprint(os.Stdout, draft.YAML)
		return nil
	}
	if interactive {
		ui.Section("This is what stamp will write to " + config.FileName + ":")
		ui.Blank()
		fmt.Fprint(ui.Out, indent(draft.YAML, "  "))
		ok, err := ui.ConfirmDefault("Write it?", true)
		if err != nil {
			return err
		}
		if !ok {
			ui.Note("Nothing was written.")
			return errQuiet
		}
	}
	if err := os.WriteFile(path, []byte(draft.YAML), 0o644); err != nil {
		return err
	}

	reportInit(draft.Config)
	return nil
}

// askInit puts init's questions. It asks about the shape of the repository
// first, one release or several, because that answer changes what every
// later default should be.
func askInit(root string, opts *config.InitOptions) error {
	found, err := config.Detect(root, *opts)
	if err != nil {
		return err
	}

	ui.Title("stamp init")
	ui.Note("Found these version files:")
	for _, f := range found {
		for _, spec := range f.Specs {
			ui.Item(spec.Shorthand())
		}
	}

	if len(found) > 1 {
		ui.Blank()
		ui.Note("They sit in %d different directories, which usually means %d things", len(found), len(found))
		ui.Note("that are released separately, each with its own version and its own tag.")
		separate, err := ui.ConfirmDefault(fmt.Sprintf("Set them up as %d components?", len(found)), true)
		if err != nil {
			return err
		}
		if !separate {
			opts.Single = true
			found = []config.Found{{Dir: "."}}
		}
	}

	ui.Blank()
	opts.Name = ui.Ask("Project name?", orElse(opts.Name, filepath.Base(root)))
	opts.Branch = ui.Ask("Release from which branch?", orElse(opts.Branch, config.DefaultBranch))
	opts.Remote = ui.Ask("Push to which remote?", orElse(opts.Remote, config.DefaultRemote))

	defaultTag := config.DefaultTag
	if len(found) > 1 && !opts.Single {
		defaultTag = "{{component}}-v{{version}}"
	}
	opts.Tag = ui.Ask("Tag template?", orElse(opts.Tag, defaultTag))
	return nil
}

// reportInit prints what the written config amounts to, with the version each
// component currently holds, the quickest way to see that init pointed at the
// right files.
func reportInit(cfg *config.Config) {
	ui.Title("Wrote %s", config.FileName)
	ui.Field("Project", cfg.ProjectName)

	var unreadable []string
	for _, comp := range cfg.Components {
		if cfg.Multi {
			ui.Blank()
			ui.Field("Component", comp.Name)
		}
		example := "1.2.3"
		current, err := comp.Source().Read()
		if err != nil {
			unreadable = append(unreadable, err.Error())
		} else {
			example = current
		}
		for i, src := range comp.Sources {
			label := "Mirror"
			value := src.Describe()
			if i == 0 {
				label = "Version"
				if err == nil {
					value = fmt.Sprintf("%s (%s)", current, src.Describe())
				}
			}
			ui.Field(label, value)
		}
		ui.Field("Tag", comp.RenderTag(example))
		if !cfg.Multi {
			ui.Field("Branch", comp.Branch)
			ui.Field("Remote", comp.Remote)
		}
	}
	if cfg.Multi {
		ui.Blank()
		ui.Field("Branch", cfg.Only().Branch)
		ui.Field("Remote", cfg.Only().Remote)
	}

	ui.Blank()
	for _, msg := range unreadable {
		ui.Note("The version cannot be read yet: %s", msg)
	}
	example := "stamp release minor --dry-run"
	if cfg.Multi {
		example = fmt.Sprintf("stamp release %s minor --dry-run", cfg.Names()[0])
	}
	ui.Note("Edit it, then run `%s` to see the plan.", example)
}

func indent(text, prefix string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix + line + "\n")
	}
	return b.String()
}

func orElse(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// cmdMigrate rewrites a .stamp.yml written in the superseded
// version.source / version.mirrors shape.
//
// It goes through the ordinary parser rather than editing the text, so what it
// writes is by construction what stamp already understood the old file to mean.
func cmdMigrate(args []string) error {
	var dryRun bool
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.BoolVar(&dryRun, "dry-run", false, "print the file instead of writing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: stamp migrate [--dry-run]")
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := gitx.Open(wd)
	if err != nil {
		return err
	}
	draft, err := config.Migrate(repo.Root)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprint(os.Stdout, draft.YAML)
		return nil
	}
	if err := os.WriteFile(filepath.Join(repo.Root, config.FileName), []byte(draft.YAML), 0o644); err != nil {
		return err
	}
	ui.Title("Rewrote %s", config.FileName)
	ui.Note("It says the same thing; the version locations are a list now.")
	return nil
}

func cmdSet(args []string) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, _, err := load()
	if err != nil {
		return err
	}
	comp, rest, err := selectComponent(cfg, fs.Args())
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: %s", releaseUsage(cfg, "set", false))
	}

	current, err := comp.Source().Read()
	if err != nil {
		return err
	}
	next, err := version.Resolve(current, rest[0])
	if err != nil {
		return err
	}
	// `set` is the manual-correction command, so it deliberately does not
	// insist that the new version be higher: fixing a mistyped version means
	// going back.
	if _, err := version.Parse(next); err != nil {
		return err
	}
	for _, src := range comp.Sources {
		if err := src.Write(next); err != nil {
			return fmt.Errorf("writing %s: %w", src.Path(), err)
		}
		ui.Step("%s → %s", src.Describe(), next)
	}
	return nil
}

// cmdNote records one user-facing change as a fragment under the changelog
// directory.
//
// It works whether or not the changelog was ever configured: running it is
// precisely what turns the feature on, because the directory it creates is what
// ChangelogEnabled looks for.
func cmdNote(args []string) error {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, repo, err := load()
	if err != nil {
		return err
	}
	comp, rest, err := selectComponent(cfg, fs.Args())
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: %s", noteUsage(cfg))
	}
	kind, err := changelog.ParseKind(rest[0])
	if err != nil {
		// ParseKind's message already lists every kind, which is the only
		// thing worth saying to somebody who mistyped one.
		return err
	}

	// The rest of the line is the text, joined rather than required to be one
	// argument, so `stamp note added Fixed the thing` works unquoted and the
	// quoted form works too.
	text := strings.TrimSpace(strings.Join(rest[1:], " "))
	if text == "" {
		if !ui.Interactive() {
			return fmt.Errorf("usage: %s", noteUsage(cfg))
		}
		text = strings.TrimSpace(ui.Ask("What changed, as a user would read it?", ""))
		if text == "" {
			return fmt.Errorf("usage: %s", noteUsage(cfg))
		}
	}

	dir := orElse(comp.Changelog.Dir, config.DefaultChangelogDir)
	path, err := changelog.Write(repo.Root, filepath.Join(repo.Root, filepath.FromSlash(dir)), comp.Name, kind, text)
	if err != nil {
		return err
	}

	ui.Blank()
	ui.Step("Wrote %s", path)
	ui.Blank()
	if cfg.Multi {
		ui.Field("Component", comp.Name)
	}
	ui.Field("Kind", kind.Heading())
	ui.Field("Entry", text)
	ui.Blank()
	ui.Note("Commit it with the change it describes. The next release renders it")
	ui.Note("into the changelog and into the tag, then deletes the file.")
	return nil
}

// noteUsage lists the kinds, because a mistyped one is the usual reason this
// line is being read.
func noteUsage(cfg *config.Config) string {
	component := ""
	if cfg.Multi {
		component = "<" + strings.Join(cfg.Names(), "|") + "> "
	}
	kinds := make([]string, 0, len(changelog.Kinds()))
	for _, k := range changelog.Kinds() {
		kinds = append(kinds, string(k))
	}
	return fmt.Sprintf("stamp note %s<%s> <text>", component, strings.Join(kinds, "|"))
}

// cmdChangelog prints the section the next release would write, under an
// "Unreleased" heading.
//
// The section goes to stdout and everything else to stderr, so the command
// pipes: `stamp changelog > notes.md` gets the markdown and nothing else. It
// exits 0 even with nothing to show; "no notes yet" is a state, not a failure.
func cmdChangelog(args []string) error {
	fs := flag.NewFlagSet("changelog", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, repo, err := load()
	if err != nil {
		return err
	}
	comp, rest, err := selectComponent(cfg, fs.Args())
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage: stamp changelog [component]")
	}

	entries, from, since, err := release.Notes(comp, repo)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		ui.Hint("Nothing noted yet, and nothing to draft from the commits.")
		ui.Hint(`Record a change with: stamp note added "…"`)
		return nil
	}
	if from == release.FromCommits {
		ui.Hint("These are drafts from the commits since %s, not entries anybody wrote.", orElse(since, "the first commit"))
		ui.Hint(`Note the real ones with: stamp note added "…"`)
	}

	// Rendered through the same code a release uses, so what this prints is
	// what the release would write. Only the heading differs: there is no
	// version to name yet and no date to stamp it with, so the rendered one is
	// stripped back off and replaced.
	section := changelog.Render("unreleased", time.Now(), entries)
	fmt.Fprintf(os.Stdout, "## Unreleased\n\n%s", changelog.Body(section))
	return nil
}

// editSection opens the rendered changelog section in the user's editor and
// returns what came back.
//
// An emptied file is honoured rather than treated as a slip: deleting
// everything is how a user says this release publishes no notes, and second
// guessing that would mean the only way out is to abort the release.
func editSection(section string) (string, error) {
	editor := orElse(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return "", fmt.Errorf("--edit needs an editor; set $VISUAL or $EDITOR")
	}

	f, err := os.CreateTemp("", "stamp-changelog-*.md")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.WriteString(section); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	// The editor is a command line, not a program name, so EDITOR="code -w"
	// works the way it does everywhere else.
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running %s: %w", editor, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	edited := strings.TrimSpace(string(data))
	if edited == "" {
		return "", nil
	}
	return edited + "\n", nil
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
	comp, rest, err := selectComponent(cfg, fs.Args())
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage: stamp current [component]")
	}
	current, err := comp.Source().Read()
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
// It compares forwards. It renders the tag template from the committed version
// and compares that to the given tag, rather than trying to strip a prefix off
// the tag. Reversing a template is ambiguous; rendering it is not.
func cmdVerify(args []string) error {
	var tag string
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.StringVar(&tag, "tag", "", "the tag to check, e.g. v0.5.0")
	if err := fs.Parse(splitFlags(args, "tag")); err != nil {
		return err
	}

	cfg, _, err := load()
	if err != nil {
		return err
	}
	rest := fs.Args()
	var comp *config.Component
	if cfg.Multi && len(rest) > 0 {
		if comp = cfg.Lookup(rest[0]); comp != nil {
			rest = rest[1:]
		}
	}
	if tag == "" && len(rest) == 1 {
		tag = rest[0] // `stamp verify v0.5.0` also works
		rest = nil
	}
	if tag == "" || len(rest) != 0 {
		return fmt.Errorf("usage: stamp verify [component] --tag <tag>")
	}
	if comp == nil {
		// A CI job knows the tag it was triggered by and nothing else, so the
		// component is worked out from the tag rather than demanded of it.
		if comp, err = componentForTag(cfg, tag); err != nil {
			return err
		}
	}

	current, err := comp.Source().Read()
	if err != nil {
		return err
	}
	expected := comp.RenderTag(current)

	ui.Blank()
	ui.Field("Tag", tag)
	if cfg.Multi {
		ui.Field("Component", comp.Name)
	}
	ui.Field("Version", fmt.Sprintf("%s (%s)", current, comp.Source().Describe()))
	ui.Field("Expected tag", expected)
	ui.Blank()

	mismatch := false
	if expected != tag {
		ui.Fail(fmt.Sprintf("tag %s does not match the committed version %s", tag, current))
		mismatch = true
	} else {
		ui.Pass("tag matches the committed version")
	}

	// The mirrors are verified too: a mirror left behind means the published
	// artifacts would disagree about their own version.
	for _, m := range comp.Mirrors() {
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

// componentForTag finds the component a tag belongs to by rendering each
// component's tag template from its committed version and looking for the one
// that comes out equal. It is the same forwards comparison verify itself makes,
// so a tag that no component would ever produce is reported with the tags they
// would.
func componentForTag(cfg *config.Config, tag string) (*config.Component, error) {
	if !cfg.Multi {
		return cfg.Only(), nil
	}
	var expected []string
	for _, comp := range cfg.Components {
		current, err := comp.Source().Read()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", comp.Name, err)
		}
		rendered := comp.RenderTag(current)
		if rendered == tag {
			return comp, nil
		}
		expected = append(expected, fmt.Sprintf("%s → %s", comp.Name, rendered))
	}
	return nil, fmt.Errorf("no component of this repository would produce the tag %s (%s)",
		tag, strings.Join(expected, ", "))
}

// cmdUpdate backs `stamp self-update` and, with checkOnly, `stamp check-update`:
// the first replaces the running binary with the latest release once its
// checksum verifies, the second only reports whether a newer one exists.
//
// Unlike every other command it does not open a repository: updating the
// installed binary has nothing to do with the project the user happens to be
// standing in.
//
// The library ships a Run() that prints its own report; stamp calls the lower
// level SelfUpdate instead so the output stays in stamp's own voice. The whole
// cycle is bounded by the updater's UpdateTimeout, so the context carries no
// deadline of its own.
func cmdUpdate(checkOnly bool) error {
	return runUpdate(context.Background(), selfupdate.Config{}, buildinfo.Version, checkOnly)
}

// runUpdate is cmdUpdate with its seams exposed, so a test can point it at a
// fake release without reaching the network.
func runUpdate(ctx context.Context, cfg selfupdate.Config, current string, checkOnly bool) error {
	up, err := newUpdater(cfg)
	if err != nil {
		return err
	}

	ui.Blank()
	if !checkOnly {
		ui.Field("Current", current)
		ui.Note("Checking for updates…")
	}

	res, err := up.SelfUpdate(ctx, current, checkOnly)
	if err != nil {
		return err
	}

	switch {
	case !upver.IsNewer(res.Latest, res.Current):
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
