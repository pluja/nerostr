# Tool versions are pinned here and used by CI too. `go run pkg@version` builds
# each tool with its own dependencies, verified against the Go checksum database.
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
ACTIONLINT    := go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
HADOLINT      := ghcr.io/hadolint/hadolint:v2.15.1@sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d
ZIZMOR        := ghcr.io/zizmorcore/zizmor:1.32.0@sha256:3014cc8f68f663935f69b3c3e0e41f5ce102b268d97db13db0f62208747f860d
CONTAINER     ?= $(shell command -v docker 2>/dev/null || command -v podman 2>/dev/null)

export GOPROXY ?= https://proxy.golang.org,direct

.PHONY: check fmt fmt-check lint test vuln tidy-check actionlint zizmor hadolint images

## check: everything CI runs
check: tidy-check fmt-check lint test vuln actionlint zizmor hadolint

## fmt: format Go code (gofmt + goimports)
fmt:
	$(GOLANGCI_LINT) fmt ./...

fmt-check:
	$(GOLANGCI_LINT) fmt --diff ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	go test -race ./...

vuln:
	$(GOVULNCHECK) ./...

tidy-check:
	go mod tidy -diff

actionlint:
	$(ACTIONLINT)

## zizmor: security audit of the GitHub workflows
zizmor:
	$(CONTAINER) run --rm -v "$(CURDIR):/repo:ro" $(ZIZMOR) --offline /repo

hadolint:
	@for f in Dockerfile wallet-rpc/Dockerfile; do \
		echo "hadolint $$f"; \
		$(CONTAINER) run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml:ro" $(HADOLINT) hadolint - < $$f || exit 1; \
	done

images:
	$(CONTAINER) build -t nerostr:dev .
	$(CONTAINER) build -t nerostr-wallet-rpc:dev wallet-rpc
