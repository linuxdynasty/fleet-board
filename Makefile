# fleet-board build and gate entry points.
#
# Every pinned tool version comes from tool-versions.env (plan row 0.10); CI
# reads the same file. Recipes run under /bin/sh and need only GNU make 3.81
# (the macOS make). Tools are built with the local Go into .bin/, which is
# gitignored; nothing here uses a copy that happens to be on PATH.

include tool-versions.env

BIN := $(CURDIR)/.bin
GOLANGCI_LINT := $(BIN)/golangci-lint
GOFUMPT := $(BIN)/gofumpt

.PHONY: tools versions

# tools builds the pinned linters into .bin/. Each is rebuilt when
# tool-versions.env changes.
tools: $(GOLANGCI_LINT) $(GOFUMPT)

$(GOLANGCI_LINT): tool-versions.env
	GOBIN=$(BIN) CGO_ENABLED=0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@touch $@

$(GOFUMPT): tool-versions.env
	GOBIN=$(BIN) CGO_ENABLED=0 go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	@touch $@

# versions prints the pins this checkout uses.
versions:
	@echo "pinned: go $(GO_VERSION), node $(NODE_MAJOR), golangci-lint $(GOLANGCI_LINT_VERSION), buf $(BUF_VERSION), gofumpt $(GOFUMPT_VERSION), typescript $(TYPESCRIPT_VERSION)"
