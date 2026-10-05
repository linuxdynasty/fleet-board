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
#   make test       go vet ./..., then go test and go test -race on the
#                   packages changed against BASE_REF (default origin/main)
#   make lint       golangci-lint on the same packages
#   make fmt        gofumpt -l -w on every Go file (fmt-check only checks)
#   make gates      everything a push needs, one command (prints tool versions)
#   make hooks      turn on the pre-push guard (.githooks/pre-push)

include tool-versions.env

# BASE_REF is data: it is exported and recipes read "$$BASE_REF" quoted, so a
# value with a space or a `;` is an unknown ref, never shell text.
BASE_REF ?= origin/main
export BASE_REF

BIN := $(CURDIR)/.bin
GOLANGCI_LINT := $(BIN)/golangci-lint
GOFUMPT := $(BIN)/gofumpt
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
ACTIONLINT := go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# The race detector needs cgo everywhere except macOS (cmd/go: "-race requires
# cgo" on Linux). Only the test binaries use it; shipped binaries stay cgo-free.
RACE_CGO := $(if $(filter Darwin,$(shell uname -s)),0,1)

.PHONY: tools versions gen build smoke proto-check ts-check mod-check fmt fmt-check
.PHONY: vet test lint actionlint selftest diff-check docs-stale gates hooks

# tools builds the pinned linters into .bin/. A stamp file per tool names the
# tool version and the Go version it was built with, so a new pin, a Go upgrade
# and a move back to an older Go each build it again, and nothing else does.
GO_BUILT_WITH := $(shell go env GOVERSION)
LINT_STAMP := $(BIN)/.golangci-lint.$(GOLANGCI_LINT_VERSION).$(GO_BUILT_WITH)
FUMPT_STAMP := $(BIN)/.gofumpt.$(GOFUMPT_VERSION).$(GO_BUILT_WITH)

tools: $(LINT_STAMP) $(FUMPT_STAMP)

$(LINT_STAMP):
	GOBIN=$(BIN) CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@rm -f $(BIN)/.golangci-lint.*
	@touch $@

$(FUMPT_STAMP):
	GOBIN=$(BIN) CGO_ENABLED=0 go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	@rm -f $(BIN)/.gofumpt.*
	@touch $@

# versions prints the pins this checkout uses.
versions:
	@echo "pinned: go $(GO_VERSION), node $(NODE_MAJOR), golangci-lint $(GOLANGCI_LINT_VERSION), buf $(BUF_VERSION), gofumpt $(GOFUMPT_VERSION), typescript $(TYPESCRIPT_VERSION)"

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
test: vet
	@pkgs="$$(scripts/changed-go-packages.sh "$$BASE_REF")" || exit 1; \
	if [ -z "$$pkgs" ]; then echo "make test: no Go package changed against $$BASE_REF; no tests to run"; exit 0; fi; \
	echo "make test: $$pkgs"; \
	CGO_ENABLED=0 go test $$pkgs && CGO_ENABLED=$(RACE_CGO) go test -race $$pkgs

lint: tools
	@pkgs="$$(scripts/changed-go-packages.sh "$$BASE_REF")" || exit 1; \
	if [ -z "$$pkgs" ]; then echo "make lint: no Go package changed against $$BASE_REF; nothing to lint"; exit 0; fi; \
	echo "make lint: $$pkgs"; \
	$(GOLANGCI_LINT) run $$pkgs

# actionlint checks the workflow files in .github/workflows.
actionlint:
	$(ACTIONLINT)

# selftest runs the seeded RED/GREEN cases of the repo's own gate scripts.
selftest: tools
	scripts/test-check-versions.sh
	scripts/test-changed-go-packages.sh
	scripts/test-check-gofumpt.sh
	scripts/test-pre-push.sh
	scripts/test-verify-agent-canon.sh
	scripts/test-smoke-binaries.sh
	scripts/test-make-base-ref.sh
	scripts/test-make-tools.sh
	scripts/test-make-gates.sh
	python3 scripts/test-check-attribution.py
	python3 scripts/test-check-file-cap.py
	python3 scripts/test-agent-roles.py
	python3 scripts/test-cursor-hook.py

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

# docs-stale is advisory: it prints reminders and always exits 0.
docs-stale:
	python3 scripts/check-docs-stale.py

# gates is the whole local gate, in the order of AGENTS.md "Local Gates". It
# prints the tool versions it used before it runs anything.
gates: tools
	@echo "== tool versions used =="
	@$(GOLANGCI_LINT) version
	@go version
	@$(BUF) --version | sed 's/^/buf /'
	@echo "node $$(node -v)"
	@$(GOFUMPT) --version | sed 's/^/gofumpt /'
	@echo "== gates (base $$BASE_REF) =="
	scripts/check-versions.sh
	$(MAKE) gen
	$(MAKE) proto-check
	$(MAKE) build
	$(MAKE) smoke
	$(MAKE) ts-check
	$(MAKE) mod-check
	$(MAKE) fmt-check
	$(MAKE) test
	$(MAKE) lint
	$(MAKE) actionlint
	scripts/check-file-cap.sh
	scripts/verify-agent-canon.sh
	python3 scripts/agent-roles.py --check
	$(MAKE) selftest
	$(MAKE) diff-check

# hooks turns on the pre-push guard for this clone and all its worktrees.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-push guard on: pushes to main are refused and pushes of a branch run make gates"
