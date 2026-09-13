#!/usr/bin/env sh
# check-gate.sh - validation gate for the canonical check suite (make check).
#
# Modes:
#   check-gate.sh record   Record the fingerprint of the current Go changes. Called by
#                          `make check` after the full suite (fmt, vet, build, test)
#                          passes; never call it directly.
#   check-gate.sh          Gate mode (default). Exits 2 with a message on stderr when
#                          the working tree contains Go changes not covered by a green
#                          `make check`; exits 0 otherwise.
#
# Claude Code wires gate mode as a Stop hook in .claude/settings.json. Other agent
# harnesses and CI can call gate mode from their own finish hooks; the stdin JSON
# (Claude Code hook payload) is optional - absent input is a normal gate request.
#
# The fingerprint hashes content (diff vs HEAD plus untracked *.go files), not mtimes,
# so file deletions are covered and `touch` cannot fool the gate. Gate mode never runs
# the suite itself - only git plumbing and a hash, so it costs milliseconds.

set -u

ROOT="${CLAUDE_PROJECT_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
MARKER="$ROOT/bin/.last-check"

sha() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi
}

fingerprint() {
	{
		git -C "$ROOT" diff HEAD -- '*.go'
		git -C "$ROOT" ls-files --others --exclude-standard -- '*.go' | LC_ALL=C sort |
			while IFS= read -r f; do
				printf '%s\n' "$f"
				cat "$ROOT/$f"
			done
	} | sha | cut -d' ' -f1
}

has_go_changes() {
	[ -n "$(git -C "$ROOT" diff --name-only HEAD -- '*.go')" ] ||
		[ -n "$(git -C "$ROOT" ls-files --others --exclude-standard -- '*.go')" ]
}

if [ "${1:-}" = "record" ]; then
	mkdir -p "$ROOT/bin"
	fingerprint >"$MARKER"
	exit 0
fi

INPUT="$(cat 2>/dev/null || true)"

# Loop guard: never re-block a continuation caused by this same hook.
if printf '%s' "$INPUT" | grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true'; then
	exit 0
fi

# Sessions with no Go changes (Q&A, planning, docs-only) are never gated.
has_go_changes || exit 0

if [ -f "$MARKER" ] && [ "$(cat "$MARKER")" = "$(fingerprint)" ]; then
	exit 0
fi

cat >&2 <<'EOF'
BLOCKED: Go files changed since the last green `make check`.
Run `make check` (fmt + vet + build + test - the canonical validation suite),
fix any failure, and re-run until green before finishing.
Do not improvise ad-hoc `go build` / `go test` / `go vet` command mixes.
EOF
exit 2
