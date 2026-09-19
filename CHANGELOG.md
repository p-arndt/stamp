# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 0.5.0 - 2026-09-19

### Added

- `hooks.after_write` in .stamp.yml runs commands such as `cargo update --workspace` after `set`, `release` and `prerelease` write the version, and the release commit includes the tracked files they change

## 0.4.1 - 2026-09-15

### Fixed

- The action publishes the notes from the annotated tag even when `actions/checkout` v5 or earlier replaced it with a lightweight tag, instead of falling back to the commit history link
- The action's `stamp-version: latest` lookup is authenticated with the workflow token, so it no longer fails with an HTTP 403 on runner pools that share a rate-limited IP.

## 0.4.0 - 2026-09-06

### Added

- Changelogs written by hand: `stamp note added "..."` saves one line per change, and the next release collects them into `CHANGELOG.md` and into the release notes
- GitHub Action: one step verifies the release tag and hands the pipeline the version, the pre-release flag and the notes from the tag. Pin it as `p-arndt/stamp@v0.4.0`, or follow the 0.x line with `@v0`.
- stamp retag moves a release tag onto HEAD when the pipeline failed, keeping the release notes that were rendered into it
