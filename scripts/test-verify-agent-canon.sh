#!/usr/bin/env bash
#
# test-verify-agent-canon.sh: seeded RED/GREEN cases for the script checks in
# scripts/verify-agent-canon.sh (every scripts/*.sh and .githooks/pre-push must
# parse and be executable, and the pre-push hook must exist).
#
# Works on a throwaway copy of this checkout (never the checkout itself) and
# points the verifier at it with FLEET_AGENT_CANON_REPO_ROOT. FLEET_VERIFY_CANON_SCRIPT
# names another verifier to run (used to show the cases fail against an older
# one). Needs python3, like the verifier. Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-verify-agent-canon.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
verifier="${FLEET_VERIFY_CANON_SCRIPT:-$root/scripts/verify-agent-canon.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
copy="$work/repo"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

# fresh: copy every tracked and untracked-but-not-ignored file (symlinks stay
# symlinks, modes are kept) into $copy.
fresh() {
  rm -rf "$copy"
  mkdir -p "$copy"
  (cd "$root" && git ls-files -z --cached --others --exclude-standard) | python3 -c '
import os, shutil, sys
src, dst = sys.argv[1], sys.argv[2]
for rel in sys.stdin.buffer.read().split(b"\0"):
    if not rel:
        continue
    rel = rel.decode()
    s = os.path.join(src, rel)
    if not os.path.lexists(s):
        continue
    d = os.path.join(dst, rel)
    os.makedirs(os.path.dirname(d), exist_ok=True)
    if os.path.islink(s):
        os.symlink(os.readlink(s), d)
    else:
        shutil.copy2(s, d)
' "$root" "$copy"
}

# expect <name> <want rc> <substring of the output, or empty>
expect() {
  local name="$1" want="$2" sub="$3" out rc
  out="$(FLEET_AGENT_CANON_REPO_ROOT="$copy" "$verifier" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want ($(printf '%s' "$out" | tail -2 | tr '\n' ' '))"
    return
  fi
  if [ -n "$sub" ]; then
    case "$out" in
      *"$sub"*) ;;
      *)
        bad "$name: output lacks '$sub' ($(printf '%s' "$out" | tail -2 | tr '\n' ' '))"
        return
        ;;
    esac
  fi
  ok
}

fresh
expect "a clean copy passes" 0 ""

fresh
printf '#!/usr/bin/env bash\nif then fi (\n' > "$copy/scripts/zz-broken.sh"
chmod +x "$copy/scripts/zz-broken.sh"
expect "a scripts/*.sh that does not parse fails" 1 "zz-broken.sh does not parse"

fresh
printf '#!/usr/bin/env bash\necho ok\n' > "$copy/scripts/zz-plain.sh"
chmod -x "$copy/scripts/zz-plain.sh"
expect "a scripts/*.sh that is not executable fails" 1 "zz-plain.sh is not executable"

fresh
chmod -x "$copy/.githooks/pre-push"
expect "a pre-push hook that is not executable fails" 1 "pre-push is not executable"

fresh
printf '#!/bin/sh\nif then fi (\n' > "$copy/.githooks/pre-push"
chmod +x "$copy/.githooks/pre-push"
expect "a pre-push hook that does not parse fails" 1 "pre-push does not parse"

fresh
rm "$copy/.githooks/pre-push"
expect "a missing pre-push hook fails" 1 "missing .githooks/pre-push"

printf 'test-verify-agent-canon: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
