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
#   3. `make doc-names` and `make doc-tables` build their own contract image: a
#      FLEET_PROTO_IMAGE left in the shell (from a test or an old run) must not
#      point them at a stale or missing image.
#
# Runs on the macOS system bash 3.2. Needs make, git and go (case 3 runs the
# pinned buf through `go run`).
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
mkdir -p "$repo/é"
: > "$repo/é/HEAD"
git -C "$repo" add -A
diff_check "a tracked HEAD under a non-ASCII directory fails (git must not quote the path)" 2 "é/HEAD"

fresh
mkdir -p "$repo/é"
: > "$repo/é/HEAD"
diff_check "an untracked HEAD under a non-ASCII directory fails" 2 "é/HEAD"

fresh
mkdir -p "$repo/é"
: > "$repo/é/notes.txt"
git -C "$repo" add -A
diff_check "a non-ASCII path with an ordinary name passes" 0 ""

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
gates="gen proto-check doc-names doc-tables capability-tests ts-check build smoke mod-check fmt-check test lint actionlint selftest diff-check docs-stale-ci"

# The four direct lines of the gates recipe (the others are $(MAKE) lines).
direct="scripts/check-versions.sh
scripts/check-file-cap.sh
scripts/verify-agent-canon.sh
python3 scripts/agent-roles.py --check"

# missing_gates <dir with a Makefile>: prints what is wrong with `make gates`:
# a gate that `make -n gates` does not run, a direct line that is gone
# ("line:<text>"), and "ignore-errors" when a recipe line of `gates` starts with
# `-` or `@-` (make -n hides that prefix, so the Makefile text is read for it).
missing_gates() {
  local dir="$1" out g line missing=""
  out="$(cd "$dir" && make -n gates 2>&1)"
  for g in $gates; do
    if ! printf '%s\n' "$out" | grep -E -q "(^|[ /])make ${g}\$"; then
      missing="$missing $g"
    fi
  done
  while IFS= read -r line; do
    if ! printf '%s\n' "$out" | grep -x -F -q "$line"; then
      missing="$missing line:$line"
    fi
  done <<EOF3
$direct
EOF3
  if awk '/^gates:/ { in_gates = 1; next }
          in_gates && /^\t/ { if ($0 ~ /^\t@?-/) bad = 1; next }
          in_gates && NF && $0 !~ /^#/ { in_gates = 0 }
          END { exit !bad }' "$dir/Makefile"; then
    missing="$missing ignore-errors"
  fi
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

while IFS= read -r line; do
  cp "$root/Makefile" "$scratch/Makefile"
  grep -v -x -F "	$line" "$root/Makefile" > "$scratch/Makefile"
  if cmp -s "$root/Makefile" "$scratch/Makefile"; then
    bad "the Makefile has no '$line' recipe line to delete from the scratch copy"
    continue
  fi
  missing="$(missing_gates "$scratch")"
  if [ "$missing" = " line:$line" ]; then ok; else bad "deleting '$line' from a scratch copy was reported as:'$missing'"; fi
done <<EOF2
$direct
EOF2

for prefix in '-' '@-'; do
  sed "s/^	\\\$(MAKE) lint\$/	${prefix}\\\$(MAKE) lint/" "$root/Makefile" > "$scratch/Makefile"
  if cmp -s "$root/Makefile" "$scratch/Makefile"; then
    bad "could not build the '${prefix}\$(MAKE) lint' variant"
    continue
  fi
  missing="$(missing_gates "$scratch")"
  if [ "$missing" = " ignore-errors" ]; then ok; else bad "'${prefix}\$(MAKE) lint' in a scratch copy was reported as:'$missing'"; fi
done

# Case 3: the recipes clear FLEET_PROTO_IMAGE, and a stale value does not break them.
for target in doc-names doc-tables; do
  recipe="$(cd "$root" && make -n "$target" 2>&1)"
  case "$recipe" in
    "FLEET_PROTO_IMAGE= python3 "*) ok ;;
    *) bad "make $target does not clear FLEET_PROTO_IMAGE: $recipe" ;;
  esac
done
out="$(cd "$root" && FLEET_PROTO_IMAGE="$work/no-such-image.json" make doc-names 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "make doc-names with a stale FLEET_PROTO_IMAGE: exit $rc ($out)"; fi

printf 'test-make-gates: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
