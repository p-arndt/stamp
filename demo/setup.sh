#!/usr/bin/env bash
# Stand up the throwaway repository the README demo is recorded in.
#
# Sourced (not executed) from demo/stamp.tape, so the `cd` at the end lands in
# the shell vhs is recording. Everything lives under $TMPDIR: a bare remote so
# the push in the demo is a real push, and a work tree named demo/ holding a
# VERSION file, a package.json mirror and a .stamp.yml tying them together.
set -e

# The recording is in English, whatever the machine's locale is.
export LC_ALL=C
export LANG=C

base="$(mktemp -d)"
cd "$base"

git init -q --bare -b main remote.git
git init -q -b main demo
cd demo

git config user.email demo@example.com
git config user.name Demo
git config commit.gpgsign false
git config tag.gpgsign false

printf '0.4.0\n' > VERSION

cat > package.json <<'JSON'
{
  "name": "demo",
  "version": "0.4.0"
}
JSON

cat > .stamp.yml <<'YAML'
version:
  - VERSION
  - package.json#version
YAML

git add -A
git commit -q -m "feat: the thing works"
git remote add origin ../remote.git
git push -q -u origin main

# A short, stable prompt: the recording should not show anyone's real one.
export PS1='\[\e[38;5;213m\]demo\[\e[0m\] $ '
