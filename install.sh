#!/usr/bin/env bash
# librarian installer — pipeable, like: curl -fsSL https://opencode.ai/install | bash
#
# Usage (agent-friendly one-liner):
#   curl -fsSL https://raw.githubusercontent.com/Christoph3573/librarian/main/install.sh | bash
#
# What it does:
#   1. clones https://github.com/Christoph3573/librarian.git (shallow, --ref)
#   2. builds with Go (requires go >= 1.26)
#   3. installs `librarian` into PATH (default: $HOME/.local/bin)
#
# Env overrides:
#   LIBRARIAN_REPO     git URL (default: https://github.com/Christoph3573/librarian.git)
#   LIBRARIAN_REF      branch/tag/commit (default: main)
#   LIBRARIAN_BIN_DIR  install dir (default: $HOME/.local/bin)
#   LIBRARIAN_SRC_DIR  checkout dir (default: mktemp; kept only on failure for debugging)
#
# Flags:
#   --ref <ref>         checkout this branch/tag/commit
#   --repo <url>        use another git URL
#   --bin-dir <dir>     install binary here
#   --force             overwrite existing binary
#   --no-modify-path    don't append export PATH to shell rc files
#   -h, --help          show this help
set -euo pipefail

REPO="${LIBRARIAN_REPO:-https://github.com/Christoph3573/librarian.git}"
REF="${LIBRARIAN_REF:-main}"
BIN_DIR="${LIBRARIAN_BIN_DIR:-$HOME/.local/bin}"
MODIFY_PATH=1
FORCE=0

usage() {
  cat >&2 <<'EOF'
librarian installer — pipeable, like: curl -fsSL https://opencode.ai/install | bash

Usage (agent-friendly one-liner):
  curl -fsSL https://raw.githubusercontent.com/Christoph3573/librarian/main/install.sh | bash

Env overrides:
  LIBRARIAN_REPO     git URL (default: https://github.com/Christoph3573/librarian.git)
  LIBRARIAN_REF      branch/tag/commit (default: main)
  LIBRARIAN_BIN_DIR  install dir (default: $HOME/.local/bin)
  LIBRARIAN_SRC_DIR  checkout dir (default: mktemp; kept only on failure for debugging)

Flags:
  --ref <ref>         checkout this branch/tag/commit
  --repo <url>        use another git URL
  --bin-dir <dir>     install binary here
  --force             overwrite existing binary
  --no-modify-path    don't append export PATH to shell rc files
  -h, --help          show this help
EOF
}

log()  { printf '%s\n' "$*" >&2; }
ok()   { printf '✓ %s\n' "$*" >&2; }
fail() { printf '✗ %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --ref)      REF="${2:?--ref needs a value}"; shift 2 ;;
    --repo)     REPO="${2:?--repo needs a value}"; shift 2 ;;
    --bin-dir)  BIN_DIR="${2:?--bin-dir needs a value}"; shift 2 ;;
    --force)    FORCE=1; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    -h|--help)  usage; exit 0 ;;
    *) fail "unknown flag: $1 (see --help)" ;;
  esac
done

# --- preflight ---------------------------------------------------------------
command -v git >/dev/null 2>&1 || fail "git not found. Install git first (https://git-scm.com/downloads)."
if ! command -v go >/dev/null 2>&1; then
  fail "go not found. Install Go >= 1.26 first (https://go.dev/dl/), then re-run this script."
fi

GO_VERSION="$(go version | awk '{print $3}' | sed 's/^go//')"
log "found go $GO_VERSION"
# semver >= check, portable across BSD (macOS) and GNU sort
ver_ge() { # ver_ge <required> <actual> → true if actual >= required
  [ "$1" = "$2" ] && return 0
  oldest="$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | head -n1)"
  [ "$oldest" = "$1" ]
}
ver_ge "1.26.0" "$GO_VERSION" \
  || fail "go >= 1.26 required (found $GO_VERSION). Update Go: https://go.dev/dl/"

mkdir -p "$BIN_DIR"
TARGET="$BIN_DIR/librarian"
if [ -e "$TARGET" ] && [ "$FORCE" -ne 1 ]; then
  log "existing install found at $TARGET — verifying it works…"
  if "$TARGET" --help >/dev/null 2>&1; then
    ok "librarian already installed at $TARGET (use --force to reinstall)"
    exit 0
  else
    log "existing binary seems broken, reinstalling…"
  fi
fi

# --- checkout ----------------------------------------------------------------
if [ -n "${LIBRARIAN_SRC_DIR:-}" ]; then
  SRC_DIR="$LIBRARIAN_SRC_DIR"
  mkdir -p "$SRC_DIR"
  CLEANUP=0
else
  SRC_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t librarian)"
  CLEANUP=1
fi
cleanup() { [ "${CLEANUP:-1}" -eq 1 ] && [ -d "${SRC_DIR:-}" ] && rm -rf "$SRC_DIR"; }
trap cleanup EXIT

log "cloning $REPO (ref: $REF)…"
# shallow clone the ref; fall back to full clone for commit SHAs
if ! git clone --depth 1 --branch "$REF" "$REPO" "$SRC_DIR/repo" 2>/dev/null; then
  git clone "$REPO" "$SRC_DIR/repo"
  git -C "$SRC_DIR/repo" checkout "$REF"
fi

# --- build -------------------------------------------------------------------
log "building librarian…"
(
  cd "$SRC_DIR/repo"
  CGO_ENABLED=0 go build -trimpath -o librarian .
)
[ -x "$SRC_DIR/repo/librarian" ] || fail "build failed: binary not produced"

install -m 0755 "$SRC_DIR/repo/librarian" "$TARGET"
ok "installed to $TARGET"
"$TARGET" --help >/dev/null 2>&1 || fail "installed binary fails to run ($TARGET --help)"

# --- PATH --------------------------------------------------------------------
case ":$PATH:" in
  *":$BIN_DIR:"*) IN_PATH=1 ;;
  *) IN_PATH=0 ;;
esac

if [ "$IN_PATH" -eq 0 ]; then
  EXPORT_LINE="export PATH=\"$BIN_DIR:\$PATH\""
  log "note: $BIN_DIR is not in PATH."
  if [ "$MODIFY_PATH" -eq 1 ] && [ -n "${HOME:-}" ]; then
    for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
      [ -f "$rc" ] || continue
      if ! grep -qF "$BIN_DIR" "$rc" 2>/dev/null; then
        printf '\n# librarian (added by install.sh)\n%s\n' "$EXPORT_LINE" >> "$rc"
        log "added $BIN_DIR to PATH in $rc"
      fi
    done
    # current shell (when piped, this only affects the subshell — print hint anyway)
    export PATH="$BIN_DIR:$PATH"
  fi
  log ""
  log "To use librarian in this shell, run:"
  log "  $EXPORT_LINE"
fi

# best-effort symlink into /usr/local/bin when writable (harmless if it fails)
if command -v install >/dev/null 2>&1 && [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
  ln -sf "$TARGET" /usr/local/bin/librarian 2>/dev/null || true
fi

# --- done --------------------------------------------------------------------
ok "librarian $($TARGET --help >/dev/null 2>&1 && echo 'works')"
log ""
log "Next steps for agents:"
log "  librarian auth --help        # 1. log in first"
log "  librarian research --help    # 2. search"
log "  librarian inspect --help     # 3. formats + availability"
log "  librarian borrow --help      # 4. download / request"
