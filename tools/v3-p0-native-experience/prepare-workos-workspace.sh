#!/bin/sh
# Create a credential-free, writable snapshot of tracked WorkOS source for P0.
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
repo=$(CDPATH= cd -- "$here/../.." && pwd -P)
root=${WORKOS_WORKSPACE_ROOTS:-"$repo/.workos/workspaces"}
case "$root" in
    /*) ;;
    *) echo 'prepare-workos-workspace: WORKOS_WORKSPACE_ROOTS must be absolute' >&2; exit 2 ;;
esac
mkdir -p "$root"
root=$(CDPATH= cd -- "$root" && pwd -P)

if [ -n "$(git -C "$repo" ls-files .workos)" ]; then
    echo 'prepare-workos-workspace: tracked .workos files need review before export' >&2
    exit 1
fi
commit=$(git -C "$repo" rev-parse --verify HEAD)
target="$root/workos-$(printf '%s' "$commit" | cut -c 1-12)"
if [ -e "$target" ] || [ -L "$target" ]; then
    echo "Existing snapshot kept unchanged: $target"
    exit 0
fi

scratch=$(mktemp -d "$root/.workos-snapshot.XXXXXXXX")
archive=$(mktemp "$root/.workos-archive.XXXXXXXX")
trap 'rm -rf "$scratch"; rm -f "$archive"' EXIT HUP INT TERM
git -C "$repo" archive --format=tar --output="$archive" "$commit"
tar -xf "$archive" -C "$scratch"
rm -f "$archive"
test ! -e "$scratch/.workos"
test ! -e "$scratch/.git"
docker run --rm --user 0:0 -v "$scratch:/target" debian:bookworm-slim \
    chown -R 10001:10001 /target
mv "$scratch" "$target"
trap - EXIT HUP INT TERM
echo "Code workspace snapshot: $target"
echo "Source commit: $commit"
echo 'Register this path for a test Project; the live checkout and .workos/lan-tls private key are not mounted.'
