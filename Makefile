# fleet-board build and gate entry points (plan row 1).
#
# Every pinned tool version comes from tool-versions.env (plan row 0.10); CI
# reads the same file. Recipes run under /bin/sh and need only GNU make 3.81
# (the macOS make). Tools are built with the local Go into .bin/, which is
# gitignored; nothing here uses a copy that happens to be on PATH.
#
# gen/ is never committed, so a fresh worktree starts with `make gen`.
#
#   make gen        buf generate into gen/ (Go and TypeScript)
#   make build      CGO_ENABLED=0 go build ./...  (make smoke then runs each binary)
#   make test       go vet ./..., then on the packages changed against BASE_REF
#                   (default origin/main): go test -shuffle=on -count=TEST_COUNT
#                   (default 3), then go test -race -shuffle=on -count=1
#   make lint       golangci-lint on the same packages
#   make vuln       govulncheck ./... (fails on a reachable known vulnerability)
#   make fuzz       each fuzz target for FUZZTIME (default 60s); their seeds
#                   already run in make test
#   make fmt        gofumpt -l -w on every Go file (fmt-check only checks)
#   make gates      everything a push needs, one command (prints tool versions)
#   make hooks      turn on the pre-push guard (.githooks/pre-push)

include tool-versions.env

# BASE_REF is data: it is exported and recipes read "$$BASE_REF" quoted, so a
# value with a space or a `;` is an unknown ref, never shell text.
BASE_REF ?= origin/main
export BASE_REF

# FUZZTIME and FUZZMINIMIZETIME are data the same way: go test -fuzztime and
# -fuzzminimizetime values, read quoted.
FUZZTIME ?= 60s
export FUZZTIME
FUZZMINIMIZETIME ?= 10s
export FUZZMINIMIZETIME

BIN := $(CURDIR)/.bin
GOLANGCI_LINT := $(BIN)/golangci-lint
GOFUMPT := $(BIN)/gofumpt
GOVULNCHECK := $(BIN)/govulncheck
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
ACTIONLINT := go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# The race detector needs cgo everywhere except macOS (cmd/go: "-race requires
# cgo" on Linux). Only the test binaries use it; shipped binaries stay cgo-free.
RACE_CGO := $(if $(filter Darwin,$(shell uname -s)),0,1)

.PHONY: tools versions gen build smoke proto-check ts-check mod-check fmt fmt-check
.PHONY: vet test fuzz lint vuln actionlint selftest diff-check doc-names doc-tables telemetry-names capability-tests
.PHONY: docs-stale docs-stale-ci gates hooks

# tools builds the pinned linters into .bin/. A stamp file per tool names the
# tool version and the Go version it was built with, so a new pin, a Go upgrade
# and a move back to an older Go each build it again, and nothing else does.
GO_BUILT_WITH := $(shell go env GOVERSION)
LINT_STAMP := $(BIN)/.golangci-lint.$(GOLANGCI_LINT_VERSION).$(GO_BUILT_WITH)
FUMPT_STAMP := $(BIN)/.gofumpt.$(GOFUMPT_VERSION).$(GO_BUILT_WITH)
VULN_STAMP := $(BIN)/.govulncheck.$(GOVULNCHECK_VERSION).$(GO_BUILT_WITH)

tools: $(LINT_STAMP) $(FUMPT_STAMP) $(VULN_STAMP)

$(LINT_STAMP):
	GOBIN=$(BIN) CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@rm -f $(BIN)/.golangci-lint.*
	@touch $@

$(FUMPT_STAMP):
	GOBIN=$(BIN) CGO_ENABLED=0 go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	@rm -f $(BIN)/.gofumpt.*
	@touch $@

$(VULN_STAMP):
	GOBIN=$(BIN) CGO_ENABLED=0 go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@rm -f $(BIN)/.govulncheck.*
	@touch $@

# versions prints the pins this checkout uses.
versions:
	@echo "pinned: go $(GO_VERSION), node $(NODE_MAJOR), golangci-lint $(GOLANGCI_LINT_VERSION), buf $(BUF_VERSION), gofumpt $(GOFUMPT_VERSION), govulncheck $(GOVULNCHECK_VERSION), typescript $(TYPESCRIPT_VERSION)"

# gen starts from an empty gen/, so a deleted or renamed proto leaves no stale
# generated code. (ts-check recreates its gen/ts/node_modules symlink.)
gen:
	rm -rf gen
	$(BUF) generate

build:
	CGO_ENABLED=0 go build ./...

# smoke builds each cmd/<name> binary and runs it with --version once.
smoke:
	scripts/smoke-binaries.sh

# proto-check is the contract compile check (docs/specs/02e-contract-rules.md).
proto-check:
	$(BUF) build
	$(BUF) lint

# ts-check compiles the generated TypeScript with the pinned typescript
# (tools/tscheck). gen/ts finds its packages through a symlink to the
# tscheck node_modules, so the check needs no copy of the generated files.
ts-check:
	@test -d gen/ts || { echo "make ts-check: gen/ts is missing; run make gen first" >&2; exit 1; }
	@echo "ts-check: node $$(node -v) (CI uses $(NODE_MAJOR))"
	cd tools/tscheck && npm ci --no-audit --no-fund
	ln -sfn ../../tools/tscheck/node_modules gen/ts/node_modules
	cd tools/tscheck && ./node_modules/.bin/tsc --version && ./node_modules/.bin/tsc -p tsconfig.json

# mod-check fails when go.mod or go.sum is not what `go mod tidy` writes.
mod-check:
	go mod tidy -diff

# fmt formats every Go file with the pinned gofumpt (same file set as fmt-check).
fmt: tools
	scripts/check-gofumpt.sh --write

fmt-check: tools
	scripts/check-gofumpt.sh

vet:
	CGO_ENABLED=0 go vet ./...

# test runs vet on everything and the tests on the changed packages
# (scripts/changed-go-packages.sh: files changed against BASE_REF plus the
# packages that import them; every package when a build input changed).
# Tests run in a random order (the seed prints as -test.shuffle N; replay it
# with -shuffle=N -count=3, since go shuffles once per test binary and a leak
# shows on a repeat) and TEST_COUNT times, so state left between tests or runs
# fails; the race pass runs once. Neither pass uses the go test cache.
# TEST_COUNT is data like BASE_REF: exported, read quoted, and anything but a
# positive integer (no leading zero) fails before go runs.
TEST_COUNT ?= 3
export TEST_COUNT

test: vet
	@case "$$TEST_COUNT" in ''|*[!0-9]*|0*) echo "make test: TEST_COUNT must be a positive integer" >&2; exit 2;; esac; \
	pkgs="$$(scripts/changed-go-packages.sh "$$BASE_REF")" || exit 1; \
	if [ -z "$$pkgs" ]; then echo "make test: no Go package changed against $$BASE_REF; no tests to run"; exit 0; fi; \
	echo "make test: $$pkgs"; \
	CGO_ENABLED=0 go test -shuffle=on -count="$$TEST_COUNT" $$pkgs && \
	CGO_ENABLED=$(RACE_CGO) go test -race -shuffle=on -count=1 $$pkgs

# fuzz runs one go test per fuzz target, since -fuzz takes a single target; a
# crash fails the run and leaves its input in the package's testdata/fuzz/.
# Workers minimize each new input before they fuzz on. At go's default limit
# (60 s per input) a run from an empty fuzz cache ($(go env GOCACHE)/fuzz)
# stalled: 303k execs in 9 s, then 0 for 20 s. FUZZMINIMIZETIME (default 10s)
# caps that wait; with it the same run resumed after about 12 s.
fuzz:
	CGO_ENABLED=0 go test -run='^$$' -fuzz='^FuzzParse$$' -fuzztime="$$FUZZTIME" -fuzzminimizetime="$$FUZZMINIMIZETIME" ./internal/hub/config/
	CGO_ENABLED=0 go test -run='^$$' -fuzz='^FuzzCheckEnrollRequest$$' -fuzztime="$$FUZZTIME" -fuzzminimizetime="$$FUZZMINIMIZETIME" ./internal/hub/api/

lint: tools
	@pkgs="$$(scripts/changed-go-packages.sh "$$BASE_REF")" || exit 1; \
	if [ -z "$$pkgs" ]; then echo "make lint: no Go package changed against $$BASE_REF; nothing to lint"; exit 0; fi; \
	echo "make lint: $$pkgs"; \
	$(GOLANGCI_LINT) run $$pkgs

# vuln checks every package (not only the changed ones: a new advisory can land
# on unchanged code) against the Go vulnerability database. It needs gen/ and
# network access to vuln.go.dev; exit 3 means a reachable vulnerability.
vuln: tools
	$(GOVULNCHECK) ./...

# actionlint checks the workflow files in .github/workflows.
actionlint:
	$(ACTIONLINT)

# selftest runs the seeded RED/GREEN cases of the repo's own gate scripts.
selftest: tools
	scripts/test-check-versions.sh
	scripts/test-changed-go-packages.sh
	scripts/test-check-gofumpt.sh
	scripts/test-pre-push.sh
	scripts/test-pre-push-fetch.sh
	scripts/test-check-pr-base.sh
	scripts/test-verify-agent-canon.sh
	scripts/test-smoke-binaries.sh
	scripts/test-make-base-ref.sh
	scripts/test-make-tools.sh
	scripts/test-make-gates.sh
	scripts/test-make-test-flags.sh
	python3 scripts/test-check-attribution.py
	python3 scripts/test-check-file-cap.py
	python3 scripts/test-agent-roles.py
	python3 scripts/test-cursor-hook.py
	python3 scripts/test-cursor-hook-edges.py
	python3 scripts/test-cursor-hook-family.py
	python3 scripts/test-cursor-hook-worktrees.py
	python3 scripts/test-proto-image.py
	python3 scripts/test-check-doc-names.py
	python3 scripts/test-check-docs-stale.py
	python3 scripts/test-gen-doc-tables.py
	python3 scripts/test-report-capability-tests.py

# diff-check fails on a file named like a git ref (HEAD, main, ...) at any path,
# tracked or not yet added: it makes `git diff HEAD` ambiguous and one was once
# committed by a stray `touch HEAD`. Then it is `git diff --check` against the
# merge base, so commits already made are checked as well as the working tree.
diff-check:
	@files="$$(git -c core.quotePath=false ls-files --cached --others --exclude-standard)" || exit 1; \
	bad="$$(printf '%s\n' "$$files" | awk -F/ '$$NF ~ /^(HEAD|FETCH_HEAD|ORIG_HEAD|MERGE_HEAD|main|master)$$/')"; \
	if [ -n "$$bad" ]; then echo "diff-check: a file is named like a git ref (git diff HEAD would be ambiguous): $$bad" >&2; exit 1; fi
	@base="$$(git merge-base "$$BASE_REF" HEAD 2>/dev/null || true)"; \
	if [ -n "$$base" ]; then git diff --check "$$base"; else git diff --check; fi

# doc-names fails when a backticked proto name in the checked docs is not in
# api/fleet (plan row 1b). It reads the contract from the pinned buf's image;
# deliberate non-proto tokens are in .agents/doc-names-allow.json. Both doc
# gates build their own image: a FLEET_PROTO_IMAGE left in the shell is cleared.
doc-names:
	FLEET_PROTO_IMAGE= python3 scripts/check-doc-names.py

# doc-tables fails when a generated table in the specs (02k, 02d, 02i, 02, and
# the 02l reason codes) no longer matches the proto comments, or a tag breaks
# the rules in 02e, "Comment tags". scripts/gen-doc-tables.py --write
# regenerates the tables.
doc-tables:
	FLEET_PROTO_IMAGE= python3 scripts/gen-doc-tables.py --check

# telemetry-names fails when a log event or metric name in the Go code is not
# in the one spec .agents/telemetry-docs.json maps its area to, or is not a
# static string (tools/telemetrynames). Spec names with no code are a report
# until plan row 17. The tool is built, not `go run`, which turns its exit 2
# (bad input) into 1.
telemetry-names:
	CGO_ENABLED=0 go build -o $(BIN)/telemetrynames ./tools/telemetrynames
	$(BIN)/telemetrynames

# capability-tests lists the v1 capabilities in 01 that no product test names
# (a Go test outside scripts/ and testdata/, or a web test; gate self-tests do
# not count). It is a report until plan row 17: findings exit 0, a broken input
# fails.
capability-tests:
	python3 scripts/report-capability-tests.py

# docs-stale prints the hook reminders and always exits 0. docs-stale-ci is the
# blocking check (plan row 1b): a change in BASE_REF..HEAD to a folder mapped in
# .agents/doc-paths.json changes one of its specs or a commit carries
# `No-Doc-Change: <reason>`, and every changed Go package has its three docs.
docs-stale:
	python3 scripts/check-docs-stale.py

docs-stale-ci:
	python3 scripts/check-docs-stale.py --ci --range "$$BASE_REF"..HEAD

# gates is the whole local gate, in the order of AGENTS.md "Local Gates". It
# prints the tool versions it used before it runs anything.
gates: tools
	@echo "== tool versions used =="
	@$(GOLANGCI_LINT) version
	@go version
	@$(BUF) --version | sed 's/^/buf /'
	@echo "node $$(node -v)"
	@$(GOFUMPT) --version | sed 's/^/gofumpt /'
	@go version -m $(GOVULNCHECK) | awk '$$1 == "mod" { print "govulncheck " $$3 }'
	@sha="$$(git rev-parse --verify -q "$$BASE_REF^{commit}")" || sha="not a commit here, so make test and make lint use every package"; \
	echo "== gates (base $$BASE_REF at $$sha) =="
	scripts/check-versions.sh
	$(MAKE) gen
	$(MAKE) proto-check
	$(MAKE) doc-names
	$(MAKE) doc-tables
	$(MAKE) telemetry-names
	$(MAKE) capability-tests
	$(MAKE) build
	$(MAKE) smoke
	$(MAKE) ts-check
	$(MAKE) mod-check
	$(MAKE) fmt-check
	$(MAKE) test
	$(MAKE) lint
	$(MAKE) vuln
	$(MAKE) actionlint
	scripts/check-file-cap.sh
	scripts/verify-agent-canon.sh
	python3 scripts/agent-roles.py --check
	$(MAKE) selftest
	$(MAKE) diff-check
	$(MAKE) docs-stale-ci

# hooks turns on the pre-push guard for this clone and all its worktrees.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-push guard on: pushes to main are refused and pushes of a branch run make gates"
