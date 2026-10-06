#!/usr/bin/env bash
#
# test-pre-push-fetch.sh: seeded RED/GREEN cases for the fetch that
# .githooks/pre-push runs before `make gates` (#107). The gate measures the
# change against origin/main, so a stale local origin/main makes the change
# look bigger or smaller than it is; the hook runs `git fetch --quiet origin
# main` first, and a failed fetch stops the push. The other hook cases are in
# scripts/test-pre-push.sh (split to stay under the 500-line cap).
#
# Same setup as test-pre-push.sh: a bare repository stands in for origin, a work
# repo holds a copy of the real hook, and a fake `make` first on PATH records
# what origin/main pointed at when the gate ran.
# Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-pre-push-fetch.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
hook_src="$root/.githooks/pre-push"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
  [ -n "${out:-}" ] && printf '  output: %s\n' "$out" >&2
}

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

remote="$work/remote.git"
repo="$work/repo"
side="$work/side"
log="$work/gate.log"
fakebin="$work/bin"
mkdir -p "$fakebin"

# The fake make records the commit origin/main named when the gate started.
cat > "$fakebin/make" <<EOF
#!/bin/sh
echo "make \$* base=\$(git rev-parse -q --verify refs/remotes/origin/main)" >> "$log"
exit 0
EOF
chmod +x "$fakebin/make"

g() { git -C "$repo" "$@"; }
remote_ref() { git --git-dir="$remote" rev-parse -q --verify "$1" 2> /dev/null || echo none; }
gate_runs() { if [ -f "$log" ]; then wc -l < "$log" | tr -d ' '; else echo 0; fi; }

# fresh: a bare origin with main, a work repo on branch feature with the hook on,
# and then main moves on origin from a second clone, so the work repo's
# origin/main is one commit behind.
fresh() {
  rm -rf "$remote" "$repo" "$side" "$log"
  git init -q --bare -b main "$remote"
  git init -q -b main "$repo"
  mkdir -p "$repo/.githooks"
  cp "$hook_src" "$repo/.githooks/pre-push"
  chmod +x "$repo/.githooks/pre-push"
  printf 'one\n' > "$repo/f.txt"
  g add -A
  g commit -q -m one
  g remote add origin "$remote"
  g push -q origin main # hooks are not on yet
  g fetch -q origin
  g config core.hooksPath .githooks
  g checkout -q -b feature
  printf 'two\n' >> "$repo/f.txt"
  g commit -q -am two
  git clone -q "$remote" "$side"
  printf 'side\n' > "$side/s.txt"
  git -C "$side" add -A
  git -C "$side" commit -q -m "main moves on"
  git -C "$side" push -q origin main
}

push() {
  out="$(cd "$repo" && PATH="$fakebin:$PATH" git push "$@" 2>&1)"
  rc=$?
}

# GREEN 1: a branch push fetches origin main before the gate, so the gate sees
# the commit main is at now, not the stale one.
fresh
stale="$(g rev-parse refs/remotes/origin/main)"
current="$(remote_ref refs/heads/main)"
push origin HEAD:feature
if [ "$stale" = "$current" ]; then
  bad "setup: origin main did not move, the case proves nothing"
elif [ "$rc" -ne 0 ]; then
  bad "fetch before the gate: exit $rc"
elif [ "$(gate_runs)" != "1" ]; then
  bad "fetch before the gate: the gate ran $(gate_runs) times, want 1"
elif [ "$(cat "$log")" != "make BASE_REF=origin/main gates base=$current" ]; then
  bad "fetch before the gate: gate saw '$(cat "$log")', want base=$current (stale was $stale)"
else
  ok
fi

# RED 1: a failed fetch stops the hook: no gate, nothing lands, and it says why.
# origin's fetch URL points nowhere while its push URL still reaches the bare
# repo, so only the fetch can fail.
fresh
g config remote.origin.url "$work/no-such-remote.git"
g config remote.origin.pushurl "$remote"
push origin HEAD:feature
if [ "$rc" -eq 0 ]; then
  bad "failed fetch: the push succeeded, want it refused"
elif [ "$(remote_ref refs/heads/feature)" != "none" ]; then
  bad "failed fetch: the branch landed anyway"
elif [ "$(gate_runs)" != "0" ]; then
  bad "failed fetch: the gate ran $(gate_runs) times, want 0"
else
  case "$out" in
    *"git fetch --quiet origin main"*"failed"*) ok ;;
    *) bad "failed fetch: output does not name the failed fetch" ;;
  esac
fi

# GREEN 2: a push that runs no gate (a tag) does not fetch, so a broken fetch
# URL does not stop it.
fresh
g config remote.origin.url "$work/no-such-remote.git"
g config remote.origin.pushurl "$remote"
g tag v0.0.1
push origin v0.0.1
if [ "$rc" -ne 0 ]; then
  bad "tag push with a broken fetch URL: exit $rc"
elif [ "$(remote_ref refs/tags/v0.0.1)" = "none" ]; then
  bad "tag push with a broken fetch URL: the tag did not land"
else
  ok
fi

printf 'test-pre-push-fetch: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
