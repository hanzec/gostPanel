#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
    printf 'dev\n'
    exit 0
fi
if [[ $(git rev-parse --is-shallow-repository) == true ]]; then
    printf 'Generating a version requires full Git history. Run git fetch --unshallow.\n' >&2
    exit 1
fi

prefix=$(<VERSION)
prefix=${prefix%$'\r'}
if [[ ! $prefix =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    printf 'VERSION must contain a major.minor prefix such as 0.1.\n' >&2
    exit 1
fi

patch=$(git rev-list --first-parent --count HEAD)
sha=$(git rev-parse HEAD)
printf '%s.%s-%.7s\n' "$prefix" "$patch" "$sha"
