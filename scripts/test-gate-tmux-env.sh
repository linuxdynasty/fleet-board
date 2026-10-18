#!/usr/bin/env bash
# Check the real gate target's exported values through a tiny recursive make.
# The scratch recipe prints only four named variables, never the environment.
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/scripts"
cp "$root/tool-versions.env" "$work/"
cp "$root/scripts/local-tmux.sh" "$root/scripts/install-tmux.sh" "$work/scripts/"

# Keep the gate's real target-specific exports, but replace its long recipe and
# tool prerequisite in this throwaway copy with a probe of recursive make.
awk '
  $0 == "gates: tools" { print "gates:"; recipe = 1; next }
  recipe && /^\t/ { next }
  recipe { recipe = 0 }
  { print }
' "$root/Makefile" > "$work/Makefile"
cat >> "$work/Makefile" <<'EOF'
gates:
	@$(MAKE) --no-print-directory gate-tmux-probe
gate-tmux-probe:
	@printf '32=%s\nlatest=%s\nrequire=%s\ntimeout=%s\n' "$$FLEET_TMUX_32" "$$FLEET_TMUX_LATEST" "$$FLEET_REQUIRE_TMUX" "$$TEST_TIMEOUT"
EOF

run() {
  out="$(cd "$work" && env -i PATH="$PATH" FLEET_TMUX_32=/wrong/32 FLEET_TMUX_LATEST=/wrong/latest FLEET_REQUIRE_TMUX=0 TEST_TIMEOUT=1s make --no-print-directory gates 2>/dev/null)"
  rc=$?
}
expected="32=$(cd "$work" && env -i PATH="$PATH" scripts/local-tmux.sh path 32)
latest=$(cd "$work" && env -i PATH="$PATH" scripts/local-tmux.sh path latest)
require=1
timeout=30m"
run
if [ "$rc" -eq 0 ] && [ "$out" = "$expected" ]; then
  printf 'test-gate-tmux-env: gate exports survive recursive make and override hostile values\n'
else
  printf 'FAIL: gate values or recursive propagation: rc=%s, output=%s, expected=%s\n' "$rc" "$out" "$expected" >&2
  exit 1
fi

# Seed a missing export. The same check must reject the hostile value.
sed '/^gates: export FLEET_TMUX_LATEST /d' "$work/Makefile" > "$work/without-latest"
mv "$work/without-latest" "$work/Makefile"
run
if [ "$rc" -eq 0 ] && [ "$out" != "$expected" ]; then
  printf 'test-gate-tmux-env: seeded missing export is detected\n'
else
  printf 'FAIL: missing latest export was not detected\n' >&2
  exit 1
fi

# Package selection needs generated imports in a fresh worktree. Execute the
# real gate's gen/prepare lines in their original order, with a selector that
# refuses to run until generation has finished.
awk '
  $0 == "gates: tools" { print "gates:"; recipe = 1; next }
  recipe && /^\t/ {
    if ($0 == "\t$(MAKE) gen" || $0 ~ /^\tscripts\/local-tmux.sh prepare /) print
    next
  }
  recipe { exit }
' "$root/Makefile" > "$work/order.mk"
cat >> "$work/order.mk" <<'EOF'
gen:
	@mkdir -p gen
	@touch gen/ready
EOF
cat > "$work/scripts/local-tmux.sh" <<'EOF'
#!/bin/sh
[ "$1" = prepare ] || exit 2
[ -f gen/ready ] || { echo 'selection ran before generation' >&2; exit 1; }
EOF
chmod +x "$work/scripts/local-tmux.sh"
out="$(cd "$work" && make --no-print-directory -f order.mk MAKE="make --no-print-directory -f order.mk" gates 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && [ -f "$work/gen/ready" ]; then
  printf 'test-gate-tmux-env: fresh gate generates before selecting packages\n'
else
  printf 'FAIL: fresh gate selection order: rc=%s, output=%s\n' "$rc" "$out" >&2
  exit 1
fi

# Removing generation must expose the missing prerequisite rather than pass.
rm "$work/gen/ready"
awk '$0 != "\t$(MAKE) gen"' "$work/order.mk" > "$work/without-gen.mk"
out="$(cd "$work" && make --no-print-directory -f without-gen.mk gates 2>&1)"
rc=$?
if [ "$rc" -ne 0 ]; then
  printf 'test-gate-tmux-env: seeded missing generation is detected\n'
else
  printf 'FAIL: missing generation was not detected\n' >&2
  exit 1
fi
