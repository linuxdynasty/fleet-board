#!/usr/bin/env bash
#
# test-bench-report.sh: seeded RED/GREEN cases for scripts/bench-report.sh, the
# base-vs-head benchmark report (#107).
#
# Builds a throwaway git repo (never this one) holding copies of the report
# scripts, changed-go-packages.sh and tool-versions.env, plus a tiny module
# whose package b has BenchmarkWork (one allocation per op at the base). Each
# case commits a change on a work branch and runs the real script against main:
#   same code, a comment changed  no allocs/op REVIEW, exit 0     (A/A)
#   one more allocation per op    the allocs/op row is REVIEW, exit 0
#   a package with no benchmark   "nothing to compare", exit 0
#   a benchmark new in head       "new" rows and the head-only note, exit 0
#   a base that does not build    the head-only note, exit 0
#   base and head samples         alternate, first side swapped each round,
#                                 each run from its package directory
#   a head benchmark that fails   exit 1, its message in the log
#   a base benchmark that fails   the head-only note, its message in the log
#   a bad BENCH_COUNT/BENCH_TIME  exit 2, nothing run, never shell text
# Time rows are not asserted: a tiny benchmark's time is noise on a busy host.
# Runs on the macOS system bash 3.2. Needs git, go and python3; the first run
# downloads the pinned benchstat.
#
# Usage: scripts/test-bench-report.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
repo="$work/repo"

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
  printf '  output: %s\n' "$out" | head -40 >&2
}

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE BASE_REF FLEET_BASE_REF GOFLAGS BENCH_COUNT BENCH_TIME
export GOWORK=off GOENV=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

g() { git -C "$repo" "$@"; }

# bench_file <allocs per op>: package b's benchmark source.
bench_file() {
  cat <<EOF
package b

import "testing"

var sink [][]byte

// BenchmarkWork allocates $1 slice(s) per op.
func BenchmarkWork(b *testing.B) {
	for b.Loop() {
		sink = sink[:0]
		for i := 0; i < $1; i++ {
			sink = append(sink, make([]byte, 64))
		}
	}
}
EOF
}

fresh() {
  rm -rf "$repo"
  mkdir -p "$repo/scripts" "$repo/b" "$repo/c"
  g init -q -b main
  cp "$root/tool-versions.env" "$repo/"
  cp "$root/scripts/bench-report.sh" "$root/scripts/bench-review.py" "$root/scripts/changed-go-packages.sh" "$repo/scripts/"
  printf 'module example.com/m\n\ngo 1.24\n' > "$repo/go.mod"
  bench_file 1 > "$repo/b/b_test.go"
  printf 'package b\n' > "$repo/b/b.go"
  printf 'package c\n\nfunc C() int { return 1 }\n' > "$repo/c/c.go"
  g add -A
  g commit -q -m base
  g checkout -q -b work
}

commit() { g add -A && g commit -q -m "$1"; }

report() {
  out="$(cd "$repo" && env BENCH_COUNT=6 BENCH_TIME=2000x "$@" scripts/bench-report.sh main 2>&1)"
  rc=$?
}

# row <unit>: the report's table row for BenchmarkWork in that unit.
row() { printf '%s\n' "$out" | grep -E "^\| Work[^|]* \| $1 \|" || true; }

# A/A: only a comment changed, so b is a changed package with the same code.
fresh
sed 's/allocates/makes/' "$repo/b/b_test.go" > "$work/t" && mv "$work/t" "$repo/b/b_test.go"
commit "comment"
report
if [ "$rc" -ne 0 ]; then
  bad "A/A: exit $rc, want 0"
elif [ -z "$(row allocs/op)" ]; then
  bad "A/A: no allocs/op row"
elif printf '%s' "$(row allocs/op)" | grep -q REVIEW; then
  bad "A/A: the allocs/op row is REVIEW"
else
  ok
fi

# RED: one more allocation per op is REVIEW, and the step still exits 0.
fresh
bench_file 2 > "$repo/b/b_test.go"
commit "two allocs"
report
if [ "$rc" -ne 0 ]; then
  bad "alloc rise: exit $rc, want 0"
elif ! printf '%s' "$(row allocs/op)" | grep -q REVIEW; then
  bad "alloc rise: the allocs/op row is not REVIEW"
else
  ok
fi

# A changed package with no benchmark: nothing to compare, nothing run.
fresh
printf 'package c\n\nfunc C() int { return 2 }\n' > "$repo/c/c.go"
commit "c only"
report
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q "nothing to compare"; then ok; else bad "no benchmark: exit $rc or no 'nothing to compare'"; fi

# A benchmark that is new in head: listed as new, with the head-only note.
fresh
mkdir -p "$repo/d"
printf 'package d\n' > "$repo/d/d.go"
bench_file 1 | sed 's/^package b/package d/; s/BenchmarkWork/BenchmarkNewWork/' > "$repo/d/d_test.go"
commit "new benchmark"
report
if [ "$rc" -ne 0 ]; then
  bad "new benchmark: exit $rc, want 0"
elif ! printf '%s' "$out" | grep -qE '^\| NewWork[^|]* \| sec/op \|.*\| new \|$'; then
  bad "new benchmark: no 'new' row"
elif ! printf '%s' "$out" | grep -q "head only"; then
  bad "new benchmark: no head-only note"
else
  ok
fi

# A base whose benchmark does not build: reported, head only, exit 0.
rm -rf "$repo"
mkdir -p "$repo/scripts" "$repo/b"
g init -q -b main
cp "$root/tool-versions.env" "$repo/"
cp "$root/scripts/bench-report.sh" "$root/scripts/bench-review.py" "$root/scripts/changed-go-packages.sh" "$repo/scripts/"
printf 'module example.com/m\n\ngo 1.24\n' > "$repo/go.mod"
printf 'package b\n' > "$repo/b/b.go"
bench_file 1 | sed 's/sink = sink\[:0\]/sink = sink[:0] + broken/' > "$repo/b/b_test.go"
commit base
g checkout -q -b work
bench_file 1 > "$repo/b/b_test.go"
commit "fixed"
report
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q "did not build or run; head only"; then ok; else bad "broken base: exit $rc or no head-only note"; fi

# Base and head take turns and swap who goes first each round; each binary runs
# from its package directory. The tag comes from testdata/, so a run from the
# wrong directory panics, and each process appends it to ORDER_LOG.
tagged_bench() {
  cat <<'EOF'
package b

import (
	"os"
	"testing"
)

func init() {
	tag, err := os.ReadFile("testdata/tag")
	if err != nil {
		panic(err)
	}
	f, err := os.OpenFile(os.Getenv("ORDER_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	_, _ = f.Write(append(tag, ' '))
	_ = f.Close()
}

func BenchmarkWork(b *testing.B) {
	for b.Loop() {
	}
}
EOF
}
fresh
mkdir -p "$repo/b/testdata"
tagged_bench > "$repo/b/b_test.go"
printf base > "$repo/b/testdata/tag"
g add -A && g commit -q -m tagged && g branch -f main HEAD
printf head > "$repo/b/testdata/tag"
printf '// head\n' >> "$repo/b/b.go"
commit "head tag"
rm -f "$work/order"
report "BENCH_COUNT=4" "ORDER_LOG=$work/order"
order="$(cat "$work/order" 2> /dev/null || true)"
if [ "$rc" -ne 0 ]; then
  bad "turns: exit $rc, want 0"
elif [ "$order" != "base head head base base head head base " ]; then
  bad "turns: run order '$order', want base and head alternating, first side swapped each round"
else
  ok
fi

# RED: a head benchmark that fails fails the script.
fresh
printf 'package b\n\nimport "testing"\n\nfunc BenchmarkWork(b *testing.B) { b.Fatal("boom") }\n' > "$repo/b/b_test.go"
commit "fatal"
report
if [ "$rc" -ne 1 ]; then
  bad "failing head benchmark: exit $rc, want 1"
elif ! printf '%s' "$out" | grep -q boom; then
  bad "failing head benchmark: the failure message is not in the log"
else
  ok
fi

# A base benchmark that fails: head only, and its message is in the log.
fresh
printf 'package b\n\nimport "testing"\n\nfunc BenchmarkWork(b *testing.B) { b.Fatal("base-boom") }\n' > "$repo/b/b_test.go"
g add -A && g commit -q -m "fatal base" && g branch -f main HEAD
bench_file 1 > "$repo/b/b_test.go"
commit "fixed"
report
if [ "$rc" -ne 0 ]; then
  bad "failing base benchmark: exit $rc, want 0"
elif ! printf '%s' "$out" | grep -q "did not build or run; head only"; then
  bad "failing base benchmark: no head-only note"
elif ! printf '%s' "$out" | grep -q base-boom; then
  bad "failing base benchmark: the failure message is not in the log"
else
  ok
fi

# RED: bad BENCH_COUNT or BENCH_TIME exits 2 before anything runs.
marker="$work/marker"
fresh
bench_file 2 > "$repo/b/b_test.go"
commit "two allocs"
for v in "BENCH_COUNT=0" "BENCH_COUNT=3; touch $marker" "BENCH_COUNT=" "BENCH_TIME=1s; touch $marker" "BENCH_TIME=-1s" "BENCH_TIME=1s -run=x"; do
  rm -f "$marker"
  report "$v"
  if [ "$rc" -ne 2 ]; then
    bad "'$v': exit $rc, want 2"
  elif [ -e "$marker" ]; then
    bad "'$v': ran as shell text"
  elif printf '%s' "$out" | grep -q "^| "; then
    bad "'$v': printed a report"
  else
    ok
  fi
done

printf 'test-bench-report: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
