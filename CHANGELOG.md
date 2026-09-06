# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 0.4.0 - 2026-09-06

### Added

- Changelogs written by hand: `stamp note added "..."` saves one line per change, and the next release collects them into `CHANGELOG.md` and into the release notes
- GitHub Action: one step verifies the release tag and hands the pipeline the version, the pre-release flag and the notes from the tag. Pin it as `p-arndt/stamp@v0.4.0`, or follow the 0.x line with `@v0`.
- stamp retag moves a release tag onto HEAD when the pipeline failed, keeping the release notes that were rendered into it
