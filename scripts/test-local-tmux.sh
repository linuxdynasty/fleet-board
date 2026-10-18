#!/usr/bin/env bash
# Seeded checks for the local gate's two pinned tmux builds. All builds are fake
# and stay in a throwaway repository; no system tmux or network is used.
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
# `make gates` exports these for its real tests. This fixture owns its own
# cache and must not mistake the gate's paths for its throwaway paths.
unset FLEET_TMUX_32 FLEET_TMUX_LATEST FLEET_REQUIRE_TMUX
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
repo="$work/repo"
mkdir -p "$repo/scripts"
cp "$root/scripts/local-tmux.sh" "$repo/scripts/"
cp "$root/tool-versions.env" "$repo/"
cat > "$repo/scripts/changed-go-packages.sh" <<'EOF'
#!/bin/sh
printf './internal/agent/tmux/...\n'
EOF
cat > "$repo/scripts/install-tmux.sh" <<'EOF'
#!/bin/sh
set -eu
echo "$1 $2" >> "$FLEET_BUILD_LOG"
if [ "${CC:-}" = /missing/cc ] || [ "${CFLAGS:-}" = -hostile ] || [ "${CONFIG_SITE:-}" = /missing/site ]; then exit 9; fi
if [ -e "$FLEET_FAIL_BUILD" ]; then exit 7; fi
if [ -n "${FLEET_SLOW_BUILD:-}" ]; then sleep 1; fi
mkdir -p "$3/bin"
printf '#!/bin/sh\nprintf "tmux %s\\n"\n' "$1" > "$3/bin/tmux"
if [ -e "${FLEET_WRONG_BUILD:-/no-such-marker}" ]; then
  printf '#!/bin/sh\necho "tmux 9.9"\n' > "$3/bin/tmux"
fi
chmod +x "$3/bin/tmux"
EOF
chmod +x "$repo/scripts/"*.sh
export FLEET_BUILD_LOG="$work/build.log" FLEET_FAIL_BUILD="$work/fail"
pass=0 fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf 'FAIL: %s\n' "$1" >&2; }
run() { out="$(cd "$repo" && scripts/local-tmux.sh "$@" 2>&1)"; rc=$?; }
expect() {
  local name="$1" want="$2" part="$3"
  if [ "$rc" -eq "$want" ] && [[ "$out" == *"$part"* ]]; then ok; else bad "$name: rc=$rc, output=$out"; fi
}

# The shared CI installer must stop at a bad tarball hash, before building.
mkdir -p "$work/fakebin"
cat > "$work/fakebin/curl" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; printf 'wrong tarball' > "$1"; exit 0; fi
  shift
done
exit 2
EOF
chmod +x "$work/fakebin/curl"
out="$(PATH="$work/fakebin:$PATH" "$root/scripts/install-tmux.sh" 3.2a "$(printf '%064d' 0)" "$work/wrong-install" 2>&1)"
rc=$?
if [ "$rc" -ne 0 ] && [ ! -e "$work/wrong-install/bin/tmux" ]; then ok; else bad "wrong tarball hash was accepted: rc=$rc, output=$out"; fi

run path 32
path32="$out"
run path latest
pathlatest="$out"
clean_path="$path32"
out="$(cd "$repo" && CC=/missing/cc CFLAGS=-hostile CPPFLAGS=-hostile LDFLAGS=-hostile CONFIG_SITE=/missing/site scripts/local-tmux.sh path 32 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && [ "$out" = "$clean_path" ]; then ok; else bad "ambient build flags changed the cache key: rc=$rc, output=$out"; fi
out="$(cd "$repo" && CC=/missing/cc CFLAGS=-hostile CPPFLAGS=-hostile LDFLAGS=-hostile CONFIG_SITE=/missing/site scripts/local-tmux.sh prepare HEAD 2>&1)"
rc=$?
expect "cold cache builds both pins" 0 "tmux 3.7c"
if [ -x "$path32" ] && [ -x "$pathlatest" ] && [ "$(wc -l < "$FLEET_BUILD_LOG" | tr -d ' ')" = 2 ]; then ok; else bad "both pin paths or build count"; fi

touch "$FLEET_FAIL_BUILD" # stands in for a cold build failing offline
run prepare HEAD
expect "warm cache works offline" 0 "tmux 3.7c"
if [ "$(wc -l < "$FLEET_BUILD_LOG" | tr -d ' ')" = 2 ]; then ok; else bad "warm cache rebuilt"; fi
out="$(cd "$repo" && FLEET_REQUIRE_TMUX=1 FLEET_TMUX_32=/wrong/32 FLEET_TMUX_LATEST=/wrong/latest scripts/local-tmux.sh prepare HEAD 2>&1)"
rc=$?
expect "gate refuses a path that could fall through to PATH tmux" 1 "does not name the pinned cache path"

printf '\n# new build input\n' >> "$repo/scripts/install-tmux.sh"
run prepare HEAD
expect "a changed build input needs a fresh build and fails clearly" 1 "build tmux"
rm "$FLEET_FAIL_BUILD"
run prepare HEAD
expect "a failed build can retry" 0 "tmux 3.7c"

touch "$work/wrong-built"
export FLEET_WRONG_BUILD="$work/wrong-built"
printf '\n# wrong version build input\n' >> "$repo/scripts/install-tmux.sh"
run path 32
wrong_build_path="$out"
run prepare HEAD
expect "wrong built version is refused before publication" 1 "built tmux has wrong version"
if [ ! -e "${wrong_build_path%/install/bin/tmux}" ]; then ok; else bad "wrong built version was published"; fi
rm "$work/wrong-built"
run prepare HEAD
expect "correct build after wrong version succeeds" 0 "tmux 3.7c"
unset FLEET_WRONG_BUILD

run path 32
new32="$out"
if [ "$new32" != "$path32" ] && [ -x "$new32" ]; then ok; else bad "build input did not change cache key"; fi

# A wrong cached executable must never be passed to the tests.
cat > "$new32" <<'EOF'
#!/bin/sh
echo 'tmux 9.9'
EOF
chmod +x "$new32"
run prepare HEAD
expect "wrong cached version is refused" 1 "wrong version"
printf '#!/bin/sh\necho "tmux 3.2a"\n' > "$new32"
chmod +x "$new32"

# A dangling key is a partial publication, never a valid warm cache.
run path latest
newlatest="$out"
link="${newlatest%/install/bin/tmux}"
rm "$link"
ln -s missing-build "$link"
run prepare HEAD
expect "partial publication is refused" 1 "incomplete cache"
rm "$link"
run prepare HEAD
expect "an incomplete entry can be rebuilt" 0 "tmux 3.7c"

# Changing a pin selects a new cache key and binary. The fake builder accepts
# the new version; the real builder checks the release tarball against its hash.
sed 's/TMUX_LATEST_VERSION=3.7c/TMUX_LATEST_VERSION=3.7d/' "$repo/tool-versions.env" > "$work/versions"
mv "$work/versions" "$repo/tool-versions.env"
run path latest
pin_changed="$out"
run prepare HEAD
expect "changed pin builds a new entry" 0 "tmux 3.7d"
if [ "$pin_changed" != "$newlatest" ] && [ -x "$pin_changed" ]; then ok; else bad "pin change did not invalidate cache"; fi

# Two cold gates may build the same key. Each may compile, but only a complete
# installation can win the symlink publication.
sed 's/TMUX_LATEST_VERSION=3.7d/TMUX_LATEST_VERSION=3.7c/' "$repo/tool-versions.env" > "$work/versions"
mv "$work/versions" "$repo/tool-versions.env"
printf '\n# concurrent build input\n' >> "$repo/scripts/install-tmux.sh"
export FLEET_SLOW_BUILD=1
(cd "$repo" && scripts/local-tmux.sh prepare HEAD > "$work/one.out" 2>&1) &
one=$!
(cd "$repo" && scripts/local-tmux.sh prepare HEAD > "$work/two.out" 2>&1) &
two=$!
wait "$one"; one_rc=$?
wait "$two"; two_rc=$?
unset FLEET_SLOW_BUILD
run prepare HEAD
if [ "$one_rc" -eq 0 ] && [ "$two_rc" -eq 0 ] && [ "$rc" -eq 0 ]; then ok; else bad "concurrent builders did not publish valid pins"; fi
nested_links="$(find "$repo/.bin/tmux-local" -mindepth 2 -type l)"
if [ -z "$nested_links" ]; then ok; else bad "concurrent publication left a link inside a published build"; fi

mv "$repo/scripts/install-tmux.sh" "$work/missing-installer"
run prepare HEAD
expect "missing installer fails clearly" 1 "cannot hash install-tmux.sh"

# A gate-script-only change must still reach real tmux tests. A fake go records
# only the flags and selected pins; the production command path runs the tests.
integration="$work/integration"
mkdir -p "$integration/scripts" "$work/go-bin"
cp "$root/scripts/local-tmux.sh" "$root/scripts/install-tmux.sh" "$integration/scripts/"
cp "$root/tool-versions.env" "$integration/"
printf '.bin/\n' > "$integration/.gitignore"
cat > "$integration/scripts/changed-go-packages.sh" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$integration/scripts/"*.sh
git -C "$integration" init -q -b main
git -C "$integration" add -A
git -C "$integration" -c user.name=test -c user.email=test@example.com commit -qm base
git -C "$integration" update-ref refs/remotes/origin/main HEAD
git -C "$integration" checkout -qb work
printf '\n# gate change\n' >> "$integration/scripts/local-tmux.sh"
cat > "$work/go-bin/go" <<'EOF'
#!/bin/sh
printf '%s | cgo=%s | 32=%s | latest=%s | require=%s\n' "$*" "$CGO_ENABLED" "$FLEET_TMUX_32" "$FLEET_TMUX_LATEST" "$FLEET_REQUIRE_TMUX" >> "$FLEET_GO_LOG"
EOF
chmod +x "$work/go-bin/go"
export FLEET_GO_LOG="$work/go.log"
out="$(cd "$integration" && PATH="$work/go-bin:$PATH" FLEET_TMUX_32=/pin/32 FLEET_TMUX_LATEST=/pin/latest FLEET_REQUIRE_TMUX=1 TEST_COUNT=3 TEST_TIMEOUT=30m scripts/local-tmux.sh extra-tests origin/main 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && [ "$(wc -l < "$FLEET_GO_LOG" | tr -d ' ')" = 2 ] &&
  rg -q -- '-count=3 .*./internal/agent/tmux/' "$FLEET_GO_LOG" &&
  rg -q -- '-race .* -count=1 .*./internal/agent/tmux/' "$FLEET_GO_LOG" &&
  rg -q -- '32=/pin/32 . latest=/pin/latest . require=1' "$FLEET_GO_LOG"; then
  ok
else
  bad "script-only gate change missed both-pin repeat and race tests: rc=$rc, output=$out"
fi

rm -f "$FLEET_GO_LOG"
out="$(cd "$integration" && PATH="$work/go-bin:$PATH" FLEET_TMUX_32=/pin/32 FLEET_REQUIRE_TMUX=1 TEST_COUNT=3 TEST_TIMEOUT=30m scripts/local-tmux.sh extra-tests origin/main 2>&1)"
rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *'need both gate pins'* ]] && [ ! -e "$FLEET_GO_LOG" ]; then ok; else bad "missing latest pin did not stop tests: rc=$rc, output=$out"; fi

# A Makefile-only or pin-only change also reaches the integration tests.
git -C "$integration" show HEAD:scripts/local-tmux.sh > "$integration/scripts/local-tmux.sh"
for changed in Makefile tool-versions.env; do
  if [ "$changed" = Makefile ]; then
    printf '# gate changed\n' > "$integration/Makefile"
  else
    rm "$integration/Makefile"
    printf '\n# pin changed\n' >> "$integration/tool-versions.env"
  fi
  rm -f "$FLEET_GO_LOG"
  out="$(cd "$integration" && PATH="$work/go-bin:$PATH" FLEET_TMUX_32=/pin/32 FLEET_TMUX_LATEST=/pin/latest FLEET_REQUIRE_TMUX=1 TEST_COUNT=3 TEST_TIMEOUT=30m scripts/local-tmux.sh extra-tests origin/main 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ] && [ -f "$FLEET_GO_LOG" ] && [ "$(wc -l < "$FLEET_GO_LOG" | tr -d ' ')" = 2 ]; then ok; else bad "$changed-only change missed tmux tests: rc=$rc, output=$out"; fi
done

printf 'test-local-tmux: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
