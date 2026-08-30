package config

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/p-arndt/stamp/internal/source"
	"github.com/p-arndt/stamp/internal/version"
)

// InitOptions are the overrides `stamp init` accepts. Every empty field is
// filled in by detection or by the package defaults.
type InitOptions struct {
	Name string
	// Versions are version locations in shorthand form ("package.json#version").
	// When any are given they replace detection entirely, and the result is a
	// single-component config.
	Versions []string

	Branch string
	Remote string
	Tag    string
	Commit string
	PreID  string

	// Single forces one component even when the repository looks like it holds
	// several. Answering "no" to init's question sets it.
	Single bool
}

// Draft is a .stamp.yml that has not been written yet: the exact bytes, plus
// the configuration they load into, so the caller can report what the file says
// without parsing it again.
type Draft struct {
	YAML   string
	Config *Config
}

// Found is one version location init detected, with the component it belongs to.
type Found struct {
	// Component is the directory the location sits in, as a component name.
	// Empty for the repository root.
	Component string
	// Dir is the directory relative to the repository root, "." for the root.
	Dir string
	// Specs are the version locations in that directory, source of truth first.
	Specs []source.Spec
}

// Init renders a .stamp.yml for the repository at root.
//
// What it writes is deliberately verbose: every key it fills in is written out
// with its value, even where that value is the default, because the point of
// the generated file is that a human reads it next and edits the two lines that
// are wrong for their project. stamp itself needs none of it.
//
// The rendered file is validated by loading it back through the normal parser,
// so init can never produce a file that stamp would reject.
func Init(root string, o InitOptions) (*Draft, error) {
	found, err := Detect(root, o)
	if err != nil {
		return nil, err
	}

	name := o.Name
	if name == "" {
		name = filepath.Base(root)
	}
	if len(found) > 1 {
		nameComponents(found, name)
	}
	d := draft{
		name:   name,
		found:  found,
		branch: orDefault(o.Branch, DefaultBranch),
		remote: orDefault(o.Remote, DefaultRemote),
		tag:    orDefault(o.Tag, DefaultTag),
		commit: orDefault(o.Commit, DefaultCommit),
		preID:  orDefault(o.PreID, version.DefaultPreID),
		push:   true,
	}
	// With several components the shared tag template has to tell them apart,
	// so the default grows a {{component}} unless the user gave their own.
	if len(found) > 1 && o.Tag == "" {
		d.tag = "{{component}}-v{{version}}"
	}

	out := d.render()
	cfg, err := parse(root, []byte(out))
	if err != nil {
		return nil, fmt.Errorf("init produced a config stamp cannot read (%w). Please report this", err)
	}
	return &Draft{YAML: out, Config: cfg}, nil
}

// Detect works out what the repository versions, and how many things it
// versions. Explicit --version flags short-circuit it.
func Detect(root string, o InitOptions) ([]Found, error) {
	if len(o.Versions) > 0 {
		var specs []source.Spec
		for _, v := range o.Versions {
			spec, err := source.ParseSpec(v)
			if err != nil {
				return nil, err
			}
			if _, err := spec.Normalize(); err != nil {
				return nil, err
			}
			specs = append(specs, spec)
		}
		return []Found{{Dir: ".", Specs: specs}}, nil
	}

	found, err := scan(root)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no version file found in %s. stamp knows %s; create one and run `stamp init` again",
			root, knownList())
	}
	if o.Single || len(found) == 1 {
		return []Found{flatten(found)}, nil
	}
	return found, nil
}

// nameComponents gives every detected group a usable, unique name. The
// repository root has no directory to be named after, so it takes the project's
// own name: in a monorepo the root is usually the thing the repository is
// called after in the first place.
func nameComponents(found []Found, project string) {
	taken := map[string]bool{}
	for i := range found {
		name := found[i].Component
		if name == "" {
			name = componentName(project)
		}
		if name == "" {
			name = "root"
		}
		for n, candidate := 2, name; ; n++ {
			if !taken[candidate] {
				name = candidate
				break
			}
			candidate = fmt.Sprintf("%s-%d", name, n)
		}
		taken[name] = true
		found[i].Component = name
	}
}

// known is what a version lives in, in the order that decides which of two
// files in the same directory is the source of truth: a plain VERSION file
// beats a manifest, because a project that keeps both keeps the plain file as
// the truth and mirrors it into the manifest.
var known = []struct {
	file  string
	field string
}{
	{file: "VERSION"},
	{file: "version.txt"},
	{file: "pyproject.toml", field: "project.version"},
	{file: "Cargo.toml", field: "package.version"},
	{file: "package.json", field: "version"},
	{file: "Chart.yaml", field: "version"},
}

func knownList() string {
	names := make([]string, len(known))
	for i, k := range known {
		names[i] = k.file
	}
	return strings.Join(names, ", ")
}

// scanDepth limits how far down init looks. Two levels below the root reaches
// the usual monorepo layouts (packages/web, services/api) without walking a
// whole dependency tree.
const scanDepth = 2

// skipDirs are never descended into: they hold other projects' manifests, and a
// version found in one of them belongs to somebody else.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true,
	"target": true, "dist": true, "build": true, ".venv": true, "venv": true,
	"third_party": true, ".idea": true, ".vscode": true,
}

// scan walks the repository and groups the version files it knows by directory.
// The root directory, if it has any, always comes first.
func scan(root string) ([]Found, error) {
	byDir := map[string][]source.Spec{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner of the tree is not init's problem
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if depthOf(rel) > scanDepth {
				return fs.SkipDir
			}
			return nil
		}
		for _, k := range known {
			if d.Name() != k.file {
				continue
			}
			dir := filepath.ToSlash(filepath.Dir(rel))
			byDir[dir] = append(byDir[dir], source.Spec{
				Path:  filepath.ToSlash(rel),
				Field: k.field,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	// The root first, then alphabetically, so the generated file is stable.
	sort.Slice(dirs, func(i, j int) bool {
		if (dirs[i] == ".") != (dirs[j] == ".") {
			return dirs[i] == "."
		}
		return dirs[i] < dirs[j]
	})

	found := make([]Found, 0, len(dirs))
	for _, dir := range dirs {
		specs := byDir[dir]
		sort.SliceStable(specs, func(i, j int) bool { return rank(specs[i].Path) < rank(specs[j].Path) })
		found = append(found, Found{Component: componentName(dir), Dir: dir, Specs: specs})
	}
	return found, nil
}

// flatten merges every detected location into one component: what a repository
// that versions a single thing in several files looks like.
func flatten(found []Found) Found {
	all := Found{Dir: "."}
	for _, f := range found {
		all.Specs = append(all.Specs, f.Specs...)
	}
	return all
}

func rank(path string) int {
	name := filepath.Base(path)
	for i, k := range known {
		if name == k.file {
			return i
		}
	}
	return len(known)
}

func depthOf(rel string) int {
	if rel == "." {
		return 0
	}
	return strings.Count(filepath.ToSlash(rel), "/") + 1
}

// componentName turns a directory into a usable component name: lowercase,
// dashes only, and the last segment of the path.
func componentName(dir string) string {
	if dir == "." || dir == "" {
		return ""
	}
	name := filepath.Base(dir)
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Migrate rewrites an existing config in the current shape, keeping everything
// it says. It is the way out of the superseded version.source / version.mirrors
// form: the file is read through the normal parser, so whatever stamp
// understood before is exactly what comes out.
func Migrate(root string) (*Draft, error) {
	cfg, err := Load(root)
	if err != nil {
		return nil, err
	}
	if !cfg.FromFile {
		return nil, fmt.Errorf("there is no %s to migrate, run `stamp init` to write one", FileName)
	}
	if !cfg.Legacy {
		return nil, fmt.Errorf("%s is already in the current shape, there is nothing to migrate", FileName)
	}

	comp := cfg.Only()
	d := draft{
		name:   cfg.ProjectName,
		branch: comp.Branch,
		remote: comp.Remote,
		tag:    comp.TagTemplate,
		commit: comp.CommitTemplate,
		preID:  comp.PreID,
		push:   comp.Push,
	}
	// A source renders back into exactly the shorthand it was built from, so
	// the round trip through ParseSpec cannot lose anything.
	found := Found{Dir: "."}
	for _, src := range comp.Sources {
		spec, err := source.ParseSpec(src.Describe())
		if err != nil {
			return nil, err
		}
		found.Specs = append(found.Specs, spec)
	}
	d.found = []Found{found}

	out := d.render()
	migrated, err := parse(root, []byte(out))
	if err != nil {
		return nil, fmt.Errorf("the migrated config does not load (%w). Please report this", err)
	}
	return &Draft{YAML: out, Config: migrated}, nil
}

// draft holds the resolved values while they are rendered.
type draft struct {
	name   string
	found  []Found
	branch string
	remote string
	tag    string
	commit string
	preID  string
	push   bool
}

// render writes the YAML by hand rather than marshalling a struct. A marshalled
// file would carry no comments and would spell out the keys in the struct's
// order rather than in the order they are worth reading.
func (d draft) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: how stamp releases this repository.\n", FileName)
	b.WriteString("# Everything here has a default; it is written out so you can see and change it.\n")
	b.WriteString("# Delete any line you are happy with.\n\n")

	fmt.Fprintf(&b, "project: %s\n\n", yamlValue(d.name))

	multi := len(d.found) > 1
	if !multi {
		b.WriteString("# Where the version lives. The first file is the source of truth;\n")
		b.WriteString("# every other one is written to match it, in the same commit.\n")
		b.WriteString("version:\n")
		for _, spec := range d.found[0].Specs {
			fmt.Fprintf(&b, "  - %s\n", yamlValue(spec.Shorthand()))
		}
		b.WriteString("\n")
	}

	b.WriteString("release:\n")
	setting(&b, "branch", d.branch, "releases are cut from here only")
	setting(&b, "remote", d.remote, "")
	setting(&b, "tag", d.tag, "")
	setting(&b, "commit", d.commit, "")
	setting(&b, "push", fmt.Sprintf("%t", d.push), "false stops after the local tag")
	setting(&b, "prerelease", d.preID, "the series `stamp prerelease` opens")

	if multi {
		b.WriteString("\n# Each component is versioned, tagged and released on its own:\n")
		b.WriteString("#   stamp release " + d.found[0].Component + " minor\n")
		b.WriteString("# Everything under release: above applies to all of them; a component\n")
		b.WriteString("# overrides only the keys it names.\n")
		b.WriteString("components:\n")
		for i, f := range d.found {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "  %s:\n", yamlValue(f.Component))
			b.WriteString("    version:\n")
			for _, spec := range f.Specs {
				fmt.Fprintf(&b, "      - %s\n", yamlValue(spec.Shorthand()))
			}
		}
	}
	return b.String()
}

// settingColumn is where the trailing comments in the generated file line up.
const settingColumn = 34

// setting writes one indented "key: value" line with its comment aligned.
func setting(b *strings.Builder, key, value, comment string) {
	line := fmt.Sprintf("  %s: %s", key, yamlValue(value))
	if comment == "" {
		b.WriteString(line + "\n")
		return
	}
	if len(line) < settingColumn {
		line += strings.Repeat(" ", settingColumn-len(line))
	} else {
		line += "  "
	}
	fmt.Fprintf(b, "%s# %s\n", line, comment)
}

// yamlValue quotes anything that is not an unambiguous plain scalar. Templates
// carry braces and colons, and a project name can be anything at all, so the
// safe rule is: quote unless the value is a bare word.
func yamlValue(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '#' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
