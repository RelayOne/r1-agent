#!/usr/bin/env bash
#
# Symlink the latest `r1` binary builds into desktop/src-tauri/binaries/
# under the per-target-triple filenames Tauri's `bundle.externalBin`
# resolver expects.
#
# Per spec desktop-cortex-augmentation §2, the desktop bundle ships the
# r1 daemon as a sidecar fallback. Tauri appends `-{triple}{ext}` to
# every `externalBin` entry, so for the single entry `binaries/r1` we
# must ensure each of these files exists at bundle time:
#
#   binaries/r1-x86_64-unknown-linux-gnu
#   binaries/r1-aarch64-apple-darwin
#   binaries/r1-x86_64-apple-darwin
#   binaries/r1-x86_64-pc-windows-msvc.exe
#   binaries/r1-aarch64-pc-windows-msvc.exe
#
# Invoked from `cargo tauri build` via `beforeBuildCommand` (or manually
# in CI before bundling). Skips triples whose source binary is absent;
# the bundler itself fails if a required triple is still missing for
# the current host target. This keeps local builds (one host triple)
# fast while cross-builds populate the rest from CI artifacts.
#
# Source layout (configurable via R1_BINARY_BUILD_ROOT):
#
#   ${R1_BINARY_BUILD_ROOT:-../../target}/release/r1                          # host build
#   ${R1_BINARY_BUILD_ROOT:-../../target}/<triple>/release/r1{,.exe}          # cross builds
#
# The host build is also linked under its detected triple so `cargo tauri
# build` on a developer's machine works without cross-compiling.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DESKTOP_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEST_DIR="$DESKTOP_DIR/src-tauri/binaries"
BUILD_ROOT="${R1_BINARY_BUILD_ROOT:-$DESKTOP_DIR/../target}"

mkdir -p "$DEST_DIR"

# All triples spec §2 requires. Each entry: "<triple>:<ext>" where ext
# is empty for unix, ".exe" for Windows.
TRIPLES=(
  "x86_64-unknown-linux-gnu:"
  "aarch64-apple-darwin:"
  "x86_64-apple-darwin:"
  "x86_64-pc-windows-msvc:.exe"
  "aarch64-pc-windows-msvc:.exe"
)

# Detect host triple (used to also link the host's `target/release/r1`).
detect_host_triple() {
  local arch kernel
  arch="$(uname -m)"
  kernel="$(uname -s)"
  case "$kernel" in
    Linux)  echo "${arch}-unknown-linux-gnu" ;;
    Darwin) echo "${arch}-apple-darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "${arch}-pc-windows-msvc" ;;
    *) echo "" ;;
  esac
}

HOST_TRIPLE="$(detect_host_triple)"
linked=0
skipped=0

link_or_copy() {
  local src="$1" dst="$2"
  rm -f "$dst"
  # Prefer symlink so subsequent r1 rebuilds propagate without rerunning
  # this script. Falls back to copy on filesystems that disallow links.
  if ln -s "$src" "$dst" 2>/dev/null; then
    return 0
  fi
  cp -f "$src" "$dst"
}

for entry in "${TRIPLES[@]}"; do
  triple="${entry%%:*}"
  ext="${entry##*:}"
  src=""
  # Cross-build layout: target/<triple>/release/r1
  candidate="$BUILD_ROOT/$triple/release/r1$ext"
  if [ -f "$candidate" ]; then
    src="$candidate"
  fi
  # Host fallback: target/release/r1 also satisfies the host triple.
  if [ -z "$src" ] && [ "$triple" = "$HOST_TRIPLE" ]; then
    candidate="$BUILD_ROOT/release/r1$ext"
    if [ -f "$candidate" ]; then
      src="$candidate"
    fi
  fi
  dst="$DEST_DIR/r1-$triple$ext"
  if [ -n "$src" ]; then
    link_or_copy "$src" "$dst"
    echo "linked $dst -> $src"
    linked=$((linked + 1))
  else
    # tauri-build resolves every externalBin path at build time, even
    # for `cargo build` (not just `cargo tauri build`). Emit an empty
    # placeholder so the build proceeds; CI overwrites with the real
    # cross-compiled artefact before bundling. The placeholder is git-
    # ignored (see ../src-tauri/binaries/.gitignore).
    : > "$dst"
    echo "stub  $dst (no source at $BUILD_ROOT/$triple/release/r1$ext)" >&2
    skipped=$((skipped + 1))
  fi
done

echo "copy-r1-binaries: linked=$linked skipped=$skipped dest=$DEST_DIR"
exit 0
