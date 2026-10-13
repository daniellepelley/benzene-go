#!/bin/sh
# Creates the annotated git tags for one release of every library module in this repo, and prints
# the command that publishes them. Go has no registry upload: pushing the tags IS the release
# (RELEASING.md). Nothing is pushed by this script.
#
#   scripts/tag-release.sh v0.1.0-beta.1
#
# The root module is tagged `<version>`; every other library module is tagged `<dir>/<version>`, Go's
# convention for nested modules. Examples and templates are not libraries and are not tagged.
#
# It refuses to tag when the tree is dirty, or when any module's go.mod requires another module of
# this repo at a version other than the one being tagged: a consumer resolving a nested module would
# then fetch a root (or sibling) version that does not exist.
set -eu

version=${1:?usage: scripts/tag-release.sh vX.Y.Z[-pre]}
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "not a semver tag: $version" >&2; exit 1 ;;
esac

cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain)" ]; then
	echo "the working tree is not clean; commit or stash first" >&2
	exit 1
fi

stale=$(grep -rhoE 'daniellepelley/benzene-go(/[a-z0-9/-]+)? v[^ ]+' --include=go.mod . |
	awk -v v="$version" '$2 != v' || true)
if [ -n "$stale" ]; then
	echo "these requires name a version other than $version; move them first:" >&2
	echo "$stale" >&2
	exit 1
fi

modules=$(go work edit -json | jq -r '.Use[].DiskPath' | sed 's:^\./::; s:/*$::' |
	grep -vE '^(examples|templates)/')

tags=""
for m in $modules; do
	if [ "$m" = "." ]; then tag=$version; else tag="$m/$version"; fi
	git tag -a "$tag" -m "$tag"
	tags="$tags $tag"
done

echo "Created:$tags"
echo
echo "Publish them, all in one push:"
echo "  git push origin$tags"
