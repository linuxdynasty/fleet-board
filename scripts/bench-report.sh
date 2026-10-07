#!/usr/bin/env bash
#
# bench-report.sh: a base-vs-head benchmark report for a change (#107; a report,
# never a gate). Prints markdown; CI appends it to the step summary.
#
# Usage: scripts/bench-report.sh [BASE_REF]   (default $BASE_REF, then origin/main)
#
# 1. The packages scripts/changed-go-packages.sh names that hold a Benchmark
#    function in a _test.go file. None: it says so and exits 0.
# 2. The merge base is checked out in a throwaway git worktree (`make gen`
#    there when it has api/). Each package's test binary is built once per
#    tree, then BENCH_COUNT rounds run one sample of each side
#    (-test.bench=. -test.benchmem -test.count=1 -test.benchtime=BENCH_TIME),
#    base and head taking turns, on the same machine.
# 3. The pinned benchstat (BENCHSTAT_VERSION in tool-versions.env, through
#    go run) compares them; scripts/bench-review.py marks REVIEW rows.
#
# Exit 0 with the report, REVIEW rows or not. A base that will not build or run
# is reported as missing (head only). Exit 1 when a head benchmark fails or a
# tool breaks; exit 2 on a bad BENCH_COUNT or BENCH_TIME. Both are data, never
# shell text. Portable to the macOS system bash 3.2.
set -euo pipefail

# -P: go list prints physical directories, and they are cut against this.
root="$(cd "$(dirname "$0")/.." && pwd -P)"
cd "$root"
base_ref="${1:-${BASE_REF:-origin/main}}"
# Unset takes the default; set but empty is an error, like any other bad value.
count="${BENCH_COUNT-10}"
benchtime="${BENCH_TIME-1s}"

case "$count" in '' | *[!0-9]* | 0*)
  echo "bench-report: BENCH_COUNT must be a positive integer" >&2
  exit 2
  ;;
esac
re_time='^[1-9][0-9]*(x|s|ms)$'
[[ "$benchtime" =~ $re_time ]] || {
  echo "bench-report: BENCH_TIME must look like 1s, 500ms or 100x" >&2
  exit 2
}

# shellcheck source=/dev/null
. "$root/tool-versions.env"
: "${BENCHSTAT_VERSION:?tool-versions.env has no BENCHSTAT_VERSION}"

# has_bench <dir>: the directory has a _test.go file that declares a benchmark.
has_bench() {
  local f
  for f in "$1"/*_test.go; do
    [ -f "$f" ] || continue
    if grep -q -E '^func Benchmark[A-Z0-9_]' "$f"; then return 0; fi
  done
  return 1
}

pkgs="$(scripts/changed-go-packages.sh "$base_ref")"
bench_pkgs=""
if [ -n "$pkgs" ]; then
  # shellcheck disable=SC2086 # one package pattern per word
  dirs="$(CGO_ENABLED=0 go list -f '{{.Dir}}' $pkgs)"
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    if has_bench "$dir"; then
      rel="${dir#"$root"}"
      bench_pkgs="$bench_pkgs .${rel:-/}"
    fi
  done <<< "$dirs"
fi
if [ -z "$bench_pkgs" ]; then
  echo "### Benchmarks: base vs head"
  echo
  echo "No changed package against $base_ref holds a benchmark; nothing to compare."
  exit 0
fi

work="$(mktemp -d)"
base_tree="$work/base"
cleanup() {
  git worktree remove --force "$base_tree" > /dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# build <tree> <pkg> <out>: the package's test binary, built once.
build() { (cd "$1" && CGO_ENABLED=0 go test -c -o "$3" "$2"); }
# sample <tree> <pkg> <bin>: one run of every benchmark, from the package
# directory as go test runs it (testdata/ is relative).
sample() {
  (cd "$1/$2" && "$3" -test.run='^$' -test.bench=. -test.benchmem -test.count=1 -test.benchtime="$benchtime")
}
head_failed() {
  echo "### Benchmarks: base vs head"
  echo
  echo "A head benchmark did not build or failed; see the job log."
  echo "bench-report: a head benchmark failed:" >&2
  cat "$work/head-err.txt" >&2 2> /dev/null || true
  tail -n 40 "$work/head.txt" >&2 2> /dev/null || true
  exit 1
}
base_broken() {
  base_note="The base benchmarks did not build or run; head only. See the job log."
  cat "$work/base-gen.log" "$work/base-err.txt" >&2 2> /dev/null || true
  tail -n 40 "$work/base.txt" >&2 2> /dev/null || true
  : > "$work/base.txt"
  base_on=""
}

: > "$work/base.txt"
: > "$work/head.txt"
: > "$work/head-err.txt"
: > "$work/base-err.txt"
base_note=""
base_on=""
if merge_base="$(git merge-base "$base_ref" HEAD 2> /dev/null)" &&
  git worktree add --quiet --detach "$base_tree" "$merge_base" > /dev/null 2>&1; then
  base_on=yes
  if ! (cd "$base_tree" && { [ ! -d api ] || make gen > "$work/base-gen.log" 2>&1; }); then base_broken; fi
else
  base_note="No merge base with $base_ref; head only."
fi

# One binary per package and tree; base_bin[i] is empty when the base has no
# benchmark in package i.
n=0
base_any=""
for p in $bench_pkgs; do
  build "$root" "$p" "$work/head-$n.test" 2>> "$work/head-err.txt" || head_failed
  base_bin[n]=""
  if [ -n "$base_on" ] && [ -d "$base_tree/$p" ] && has_bench "$base_tree/$p"; then
    if build "$base_tree" "$p" "$work/base-$n.test" 2>> "$work/base-err.txt"; then
      base_bin[n]="$work/base-$n.test"
      base_any=yes
    else
      base_broken
    fi
  fi
  n=$((n + 1))
done
if [ -n "$base_on" ] && [ -z "$base_any" ]; then
  base_note="The base ($merge_base) has none of these benchmarks yet; head only."
  base_on=""
fi

# Base and head take turns, one sample each per round, and swap who goes first
# every round, so a machine that drifts (heat, a neighbor's load) moves both.
round=1
while [ "$round" -le "$count" ]; do
  order="head base"
  if [ $((round % 2)) -eq 1 ]; then order="base head"; fi
  i=0
  for p in $bench_pkgs; do
    for side in $order; do
      if [ "$side" = head ]; then
        sample "$root" "$p" "$work/head-$i.test" >> "$work/head.txt" 2>> "$work/head-err.txt" || head_failed
      elif [ -n "$base_on" ] && [ -n "${base_bin[i]}" ]; then
        sample "$base_tree" "$p" "${base_bin[i]}" >> "$work/base.txt" 2>> "$work/base-err.txt" || base_broken
      fi
    done
    i=$((i + 1))
  done
  round=$((round + 1))
done

benchstat() {
  go run "golang.org/x/perf/cmd/benchstat@$BENCHSTAT_VERSION" "$@"
}
benchstat -format csv base="$work/base.txt" head="$work/head.txt" > "$work/stat.csv"
benchstat base="$work/base.txt" head="$work/head.txt" > "$work/stat.txt"
python3 scripts/bench-review.py < "$work/stat.csv" || exit 1
echo
if [ -n "$base_note" ]; then
  echo "$base_note"
  echo
fi
echo "Packages:$bench_pkgs. Base $base_ref, BENCH_COUNT=$count, BENCH_TIME=$benchtime."
echo
echo "<details><summary>benchstat output</summary>"
echo
echo '```'
cat "$work/stat.txt"
echo '```'
echo
echo "</details>"
