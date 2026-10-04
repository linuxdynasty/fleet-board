#!/usr/bin/env bash
#
# test-pre-push.sh: seeded RED/GREEN cases for .githooks/pre-push.
#
# Everything happens in a temp dir: a bare repository stands in for the remote
# (never origin), a work repo holds a copy of the real hook, and a fake `make`
# on PATH records what the hook runs and says whether the gate passes. RED cases: a push to main (or master),
# or a delete of main, is refused and nothing lands on the bare repo. GREEN
# cases: a branch push runs the gate and lands; a failing gate blocks it.
# Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-pre-push.sh        exit 0 only if every case passes
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

# Keep git from reading the caller's repo or config, and never ask for a password.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

if [ ! -f "$hook_src" ]; then
  printf 'FAIL: %s does not exist\n' "$hook_src" >&2
  exit 1
fi

remote="$work/remote.git"
repo="$work/repo"
log="$work/gate.log"
envlog="$work/gate.env"
fakebin="$work/bin"
mkdir -p "$fakebin"

# A fake make: records its arguments and the directory it ran in, then exits
# with the code in $work/make.rc (0 when the file is absent). It stands in for
# the real `make gates`, which is far too slow for a self-test. The hook has no
# other way to be told what the gate is: this fake on PATH is the only switch.
makerc="$work/make.rc"
makeaction="$work/make.action"
cat > "$fakebin/make" <<EOF
#!/bin/sh
echo "make \$* in \$(pwd -P)" >> "$log"
echo "BASE_REF=[\${BASE_REF-}] FLEET_GOFUMPT=[\${FLEET_GOFUMPT-}] FLEET_ANY=[\${FLEET_ANY-}] MAKEFLAGS=[\${MAKEFLAGS-}] MFLAGS=[\${MFLAGS-}] MAKEOVERRIDES=[\${MAKEOVERRIDES-}] MAKELEVEL=[\${MAKELEVEL-}] GOFLAGS=[\${GOFLAGS-}] GIT_DIR=[\${GIT_DIR-}] GIT_WORK_TREE=[\${GIT_WORK_TREE-}] GIT_INDEX_FILE=[\${GIT_INDEX_FILE-}]" >> "$envlog"
if [ -f "$makeaction" ]; then
  case "\$(cat "$makeaction")" in
    touch) echo changed-by-the-gate > gate-made-this.txt ;;
    commit) git commit -q --allow-empty -m "moved by the gate" ;;
  esac
fi
if [ -f "$makerc" ]; then exit "\$(cat "$makerc")"; fi
exit 0
EOF
chmod +x "$fakebin/make"

g() { git -C "$repo" "$@"; }
remote_ref() { git --git-dir="$remote" rev-parse -q --verify "$1" 2> /dev/null || echo none; }
gate_runs() { if [ -f "$log" ]; then wc -l < "$log" | tr -d ' '; else echo 0; fi; }

# fresh: a bare remote with main, and a work repo that has the hook enabled the
# way `make hooks` does (core.hooksPath .githooks).
fresh() {
  rm -rf "$remote" "$repo" "$log" "$envlog" "$makerc" "$makeaction" "$work/wt"
  git init -q --bare -b main "$remote"
  # Let the bare repo accept deleting its current branch, so that only the hook
  # stands between a delete of main and the remote.
  git --git-dir="$remote" config receive.denyDeleteCurrent ignore
  git init -q -b main "$repo"
  mkdir -p "$repo/.githooks"
  cp "$hook_src" "$repo/.githooks/pre-push"
  chmod +x "$repo/.githooks/pre-push"
  printf 'one\n' > "$repo/f.txt"
  g add -A
  g commit -q -m one
  g remote add origin "$remote"
  g push -q origin main # hooks are not on yet
  g config core.hooksPath .githooks
  g checkout -q -b feature
  printf 'two\n' >> "$repo/f.txt"
  g commit -q -am two
}

# push <repo dir> <args...>: sets out and rc; PATH has the fake make first.
push() {
  local dir="$1"
  shift
  out="$(cd "$dir" && PATH="$fakebin:$PATH" git push "$@" 2>&1)"
  rc=$?
}

expect_refused() { # name  (uses rc, out)
  if [ "$rc" -eq 0 ]; then
    bad "$1: the push succeeded, want it refused"
    return 1
  fi
  return 0
}

expect_out() { # name substring
  case "$out" in
    *"$2"*) return 0 ;;
    *)
      bad "$1: output lacks '$2'"
      return 1
      ;;
  esac
}

main_before=""

# RED 1: a push to main is refused, main does not move, the gate does not run.
fresh
main_before="$(remote_ref refs/heads/main)"
push "$repo" origin HEAD:main
if expect_refused "push to main"; then
  if [ "$(remote_ref refs/heads/main)" != "$main_before" ]; then
    bad "push to main: main on the remote moved"
  elif [ "$(gate_runs)" != "0" ]; then
    bad "push to main: the gate ran for a refused push"
  elif expect_out "push to main" "main"; then
    ok
  fi
fi

# RED 2: pushing under another local name is refused too.
fresh
main_before="$(remote_ref refs/heads/main)"
push "$repo" origin feature:main
if expect_refused "feature:main"; then
  if [ "$(remote_ref refs/heads/main)" != "$main_before" ]; then bad "feature:main: main moved"; else ok; fi
fi

# RED 3: the full ref name is refused.
fresh
main_before="$(remote_ref refs/heads/main)"
push "$repo" origin HEAD:refs/heads/main
if expect_refused "refs/heads/main"; then
  if [ "$(remote_ref refs/heads/main)" != "$main_before" ]; then bad "refs/heads/main: main moved"; else ok; fi
fi

# RED 4: deleting main is refused and main still exists.
fresh
main_before="$(remote_ref refs/heads/main)"
push "$repo" origin :main
if expect_refused "delete main"; then
  if [ "$(remote_ref refs/heads/main)" != "$main_before" ]; then bad "delete main: main is gone or moved"; else ok; fi
fi

# RED 5: master is refused too (a repository that still uses the old name).
fresh
push "$repo" origin HEAD:master
if expect_refused "push to master"; then
  if [ "$(remote_ref refs/heads/master)" != "none" ]; then bad "push to master: master landed"; else ok; fi
fi

# RED 6: one refused ref stops the whole push, including the allowed branch.
fresh
push "$repo" origin HEAD:other HEAD:main
if expect_refused "mixed push"; then
  if [ "$(remote_ref refs/heads/other)" != "none" ]; then bad "mixed push: the branch landed anyway"; else ok; fi
fi

# GREEN 1: a branch push runs `make gates` from the repo top and lands.
fresh
push "$repo" origin HEAD:feature
head="$(g rev-parse HEAD)"
expected_dir="$(cd "$repo" && pwd -P)"
if [ "$rc" -ne 0 ]; then
  bad "branch push: exit $rc"
elif [ "$(remote_ref refs/heads/feature)" != "$head" ]; then
  bad "branch push: the branch did not land"
elif [ "$(gate_runs)" != "1" ]; then
  bad "branch push: the gate ran $(gate_runs) times, want 1"
elif [ "$(cat "$log")" != "make BASE_REF=origin/main gates in $expected_dir" ]; then
  bad "branch push: gate line was '$(cat "$log")', want 'make BASE_REF=origin/main gates in $expected_dir'"
else
  ok
fi

# GREEN 2: a gate that exits 1 blocks the push and the branch does not land.
fresh
printf '1\n' > "$makerc"
push "$repo" origin HEAD:feature
if expect_refused "failing gate"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "failing gate: the branch landed anyway"
  elif [ "$(gate_runs)" != "1" ]; then
    bad "failing gate: the gate ran $(gate_runs) times, want 1"
  elif expect_out "failing gate" "gate failed"; then
    ok
  fi
fi

# RED 7: there is no way around the gate. An environment variable that the
# first version of the hook honored (FLEET_PRE_PUSH_GATE=true) changes nothing.
fresh
printf '1\n' > "$makerc"
FLEET_PRE_PUSH_GATE=true push "$repo" origin HEAD:feature
if expect_refused "gate override variable"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "gate override variable: the branch landed with a failing gate"
  else
    ok
  fi
fi

# GREEN 4: deleting a branch (not main) is allowed and runs no gate.
fresh
push "$repo" origin HEAD:feature
rm -f "$log"
push "$repo" origin :feature
if [ "$rc" -ne 0 ]; then
  bad "delete branch: exit $rc"
elif [ "$(remote_ref refs/heads/feature)" != "none" ]; then
  bad "delete branch: the branch is still there"
elif [ "$(gate_runs)" != "0" ]; then
  bad "delete branch: the gate ran for a delete"
else
  ok
fi

# GREEN 5: a tag push is allowed and runs no gate.
fresh
g tag v0.0.1
push "$repo" origin v0.0.1
if [ "$rc" -ne 0 ]; then
  bad "tag push: exit $rc"
elif [ "$(remote_ref refs/tags/v0.0.1)" = "none" ]; then
  bad "tag push: the tag did not land"
elif [ "$(gate_runs)" != "0" ]; then
  bad "tag push: the gate ran for a tag"
else
  ok
fi

# GREEN 6: the guard works from a linked worktree (the way agents work here).
fresh
g worktree add -q -b wt-branch "$work/wt" feature
printf 'three\n' >> "$work/wt/f.txt"
git -C "$work/wt" commit -q -am three
push "$work/wt" origin HEAD:wt-branch
expected_dir="$(cd "$work/wt" && pwd -P)"
if [ "$rc" -ne 0 ]; then
  bad "worktree branch push: exit $rc"
elif [ "$(cat "$log")" != "make BASE_REF=origin/main gates in $expected_dir" ]; then
  bad "worktree branch push: gate line '$(cat "$log")', want 'make BASE_REF=origin/main gates in $expected_dir'"
else
  main_before="$(remote_ref refs/heads/main)"
  rm -f "$log"
  push "$work/wt" origin HEAD:main
  if expect_refused "worktree push to main"; then
    if [ "$(remote_ref refs/heads/main)" != "$main_before" ] || [ "$(gate_runs)" != "0" ]; then
      bad "worktree push to main: main moved or the gate ran"
    else
      ok
    fi
  fi
fi

# RED 8: pushing a commit that is not HEAD is refused, because the gate checks
# the checked-out files and could not vouch for it. Nothing lands, no gate runs.
fresh
g checkout -q -b scratch
printf 'x\n' >> "$repo/f.txt"
g commit -q -am scratch
push "$repo" origin feature:feature
if expect_refused "push of a non-HEAD commit"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "push of a non-HEAD commit: the branch landed anyway"
  elif [ "$(gate_runs)" != "0" ]; then
    bad "push of a non-HEAD commit: the gate ran for a refused push"
  elif expect_out "push of a non-HEAD commit" "Check out the branch you want to push"; then
    ok
  fi
fi

# GREEN 7: the same push is fine once that branch is checked out.
g checkout -q feature
push "$repo" origin feature:feature
if [ "$rc" -ne 0 ]; then
  bad "push of the checked-out branch: exit $rc"
elif [ "$(remote_ref refs/heads/feature)" != "$(g rev-parse feature)" ]; then
  bad "push of the checked-out branch: the branch did not land"
else
  ok
fi

# RED 9: the environment cannot steer the gate. BASE_REF=HEAD would make the
# gate see "no change"; FLEET_* variables point the gate scripts at other trees
# or binaries; MAKEFLAGS=n only prints the recipes, MFLAGS/MAKEOVERRIDES/
# MAKELEVEL change how make runs them, and GOFLAGS=-run=NoSuch turns a failing
# go test into "no tests to run". The hook scrubs them all and names the base
# itself on the command line.
fresh
empty_env="BASE_REF=[] FLEET_GOFUMPT=[] FLEET_ANY=[] MAKEFLAGS=[] MFLAGS=[] MAKEOVERRIDES=[] MAKELEVEL=[] GOFLAGS=[] GIT_DIR=[] GIT_WORK_TREE=[] GIT_INDEX_FILE=[]"
BASE_REF=HEAD FLEET_GOFUMPT=true FLEET_ANY=x MAKEFLAGS=n MFLAGS=-n MAKEOVERRIDES=BASE_REF=HEAD MAKELEVEL=3 GOFLAGS=-run=NoSuch push "$repo" origin HEAD:feature
if [ "$rc" -ne 0 ]; then
  bad "scrubbed environment: exit $rc"
elif [ "$(gate_runs)" != "1" ]; then
  bad "scrubbed environment: the gate ran $(gate_runs) times, want 1"
elif [ "$(cat "$envlog")" != "$empty_env" ]; then
  bad "scrubbed environment: the gate saw '$(cat "$envlog")', want every variable empty"
elif [ "$(cat "$log")" != "make BASE_REF=origin/main gates in $(cd "$repo" && pwd -P)" ]; then
  bad "scrubbed environment: gate line '$(cat "$log")'"
else
  ok
fi

# RED 10: a dirty tree is refused, because the gate checks the working tree and
# could only vouch for the committed HEAD if nothing else is changed.
fresh
printf 'edit\n' >> "$repo/f.txt"
push "$repo" origin HEAD:feature
if expect_refused "dirty tracked file"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "dirty tracked file: the branch landed anyway"
  elif [ "$(gate_runs)" != "0" ]; then
    bad "dirty tracked file: the gate ran for a refused push"
  elif expect_out "dirty tracked file" "Commit or remove these first" && expect_out "dirty tracked file" "f.txt"; then
    ok
  fi
fi

fresh
printf 'new\n' > "$repo/new.txt"
push "$repo" origin HEAD:feature
if expect_refused "untracked file"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "untracked file: the branch landed anyway"
  elif expect_out "untracked file" "Commit or remove these first" && expect_out "untracked file" "new.txt"; then
    ok
  fi
fi

fresh
printf 'edit\n' >> "$repo/f.txt"
g add f.txt
push "$repo" origin HEAD:feature
if expect_refused "staged but uncommitted change"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then bad "staged change: the branch landed anyway"; else ok; fi
fi

fresh
i=1
while [ "$i" -le 12 ]; do
  printf 'x\n' > "$repo/junk$i.txt"
  i=$((i + 1))
done
push "$repo" origin HEAD:feature
if expect_refused "twelve untracked files"; then
  listed="$(printf '%s\n' "$out" | grep -c 'junk[0-9]*\.txt')"
  if [ "$listed" != "10" ]; then
    bad "twelve untracked files: $listed paths listed, want 10"
  elif expect_out "twelve untracked files" "and 2 more"; then
    ok
  fi
fi

# RED 11: the tree and HEAD are checked again after the gate. A gate that
# leaves a new file behind, or moves HEAD, has not vouched for what is pushed.
fresh
printf 'touch\n' > "$makeaction"
push "$repo" origin HEAD:feature
if expect_refused "tree changed while the gate ran"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "tree changed while the gate ran: the branch landed anyway"
  elif expect_out "tree changed while the gate ran" "changed while the gate ran"; then
    ok
  fi
fi

fresh
printf 'commit\n' > "$makeaction"
push "$repo" origin HEAD:feature
if expect_refused "HEAD moved while the gate ran"; then
  if [ "$(remote_ref refs/heads/feature)" != "none" ]; then
    bad "HEAD moved while the gate ran: the branch landed anyway"
  elif expect_out "HEAD moved while the gate ran" "run it again"; then
    ok
  fi
fi

# GREEN 8: an ignored file is not a change (gen/ and .bin/ are ignored in the
# real repository).
fresh
printf 'ignored.out\n' > "$repo/.gitignore"
g add .gitignore
g commit -q -m "ignore"
printf 'x\n' > "$repo/ignored.out"
push "$repo" origin HEAD:feature
if [ "$rc" -ne 0 ]; then
  bad "ignored file: exit $rc"
elif [ "$(remote_ref refs/heads/feature)" != "$(g rev-parse HEAD)" ]; then
  bad "ignored file: the branch did not land"
else
  ok
fi

# GREEN 9: a dirty tree does not block a push that runs no gate (a tag).
fresh
g tag v0.0.2
printf 'edit\n' >> "$repo/f.txt"
push "$repo" origin v0.0.2
if [ "$rc" -ne 0 ]; then bad "tag push with a dirty tree: exit $rc"; else ok; fi

printf 'test-pre-push: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
