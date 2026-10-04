#!/usr/bin/env bash
#
# test-make-gates.sh: seeded RED/GREEN cases for two properties of the Makefile.
#
#   1. `make diff-check` fails when a file is named like a git ref (HEAD,
#      FETCH_HEAD, ORIG_HEAD, MERGE_HEAD, main, master) at any path, tracked or
#      untracked and not ignored. Such a file makes `git diff HEAD` and
#      `git log HEAD` fail as ambiguous; one was committed once by a stray
#      `touch HEAD`. Run in a throwaway git repo, never in this checkout.
#   2. `make gates` lists every gate. `make -n gates` is read from a copy of the
#      Makefile; deleting one `$(MAKE) <gate>` line from a scratch copy must be
#      caught.
#
# Runs on the macOS system bash 3.2. Needs make, git and go (`go env` only).
#
# Usage: scripts/test-make-gates.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE BASE_REF
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

# ---- 1. files named like refs ----------------------------------------------
repo="$work/repo"

# fresh: a git repo holding a copy of the Makefile and one ordinary file.
fresh() {
  rm -rf "$repo"
  mkdir -p "$repo/docs"
  git -C "$repo" init -q
  cp "$root/Makefile" "$root/tool-versions.env" "$repo/"
  printf 'notes\n' > "$repo/docs/notes.txt"
  git -C "$repo" add -A
}

# diff_check <name> <want rc> <substring of the output, or empty>
diff_check() {
  local name="$1" want="$2" sub="$3" out rc
  out="$(cd "$repo" && make diff-check 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want ($out)"
    return
  fi
  if [ -n "$sub" ]; then
    case "$out" in
      *"$sub"*) ;;
      *)
        bad "$name: output lacks '$sub' ($out)"
        return
        ;;
    esac
  fi
  ok
}

fresh
diff_check "a clean repo passes" 0 ""

for ref in HEAD FETCH_HEAD ORIG_HEAD MERGE_HEAD main master; do
  fresh
  : > "$repo/$ref"
  git -C "$repo" add -- "$ref"
  diff_check "a tracked file named $ref fails and is named" 2 "$ref"
done

fresh
mkdir -p "$repo/deep/er"
: > "$repo/deep/er/HEAD"
git -C "$repo" add -A
diff_check "a tracked file named HEAD in a subdirectory fails" 2 "deep/er/HEAD"

fresh
: > "$repo/HEAD"
diff_check "an untracked file named HEAD fails before it is ever added" 2 "HEAD"

fresh
printf 'HEAD\n' > "$repo/.gitignore"
: > "$repo/HEAD"
git -C "$repo" add .gitignore
diff_check "an ignored file named HEAD is not this gate's business" 0 ""

fresh
: > "$repo/HEADER"
: > "$repo/docs/main.go"
: > "$repo/heads"
git -C "$repo" add -A
diff_check "names that merely contain a ref name pass" 0 ""

# ---- 2. make gates lists every gate -----------------------------------------
gates="gen proto-check ts-check build smoke mod-check fmt-check test lint actionlint selftest diff-check"

# missing_gates <dir with a Makefile>: prints the gates make -n gates does not run.
missing_gates() {
  local dir="$1" out g missing=""
  out="$(cd "$dir" && make -n gates 2>&1)"
  for g in $gates; do
    if ! printf '%s\n' "$out" | grep -E -q "(^|[ /])make ${g}\$"; then
      missing="$missing $g"
    fi
  done
  printf '%s' "$missing"
}

scratch="$work/gates"
mkdir -p "$scratch"
cp "$root/Makefile" "$root/tool-versions.env" "$scratch/"
missing="$(missing_gates "$scratch")"
if [ -z "$missing" ]; then ok; else bad "make gates does not run:$missing"; fi

for g in $gates; do
  cp "$root/Makefile" "$scratch/Makefile"
  sed "/^	\$(MAKE) ${g}\$/d" "$root/Makefile" > "$scratch/Makefile"
  if cmp -s "$root/Makefile" "$scratch/Makefile"; then
    bad "the Makefile has no '\$(MAKE) $g' line to delete from the scratch copy"
    continue
  fi
  missing="$(missing_gates "$scratch")"
  if [ "$missing" = " $g" ]; then ok; else bad "deleting \$(MAKE) $g from a scratch copy was reported as missing:'$missing'"; fi
done

printf 'test-make-gates: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
