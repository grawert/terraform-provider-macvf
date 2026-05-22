PROVIDER_NAME      := macvf
PROVIDER_NAMESPACE := bushtaxi
BINARY             := terraform-provider-macvf
NETWORK_RUNNER     := internal/provider/embedded/network-runner
VFKIT              := internal/provider/embedded/vfkit

GIT_VERSION        := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS            := -s -w \
                      -X main.version=$(GIT_VERSION) \
                      -X main.providerName=$(PROVIDER_NAME) \
                      -X main.providerNamespace=$(PROVIDER_NAMESPACE)

# The provider only runs on Apple Silicon, so build always cross-compiles
# to darwin/arm64 regardless of the host. lint/test stay on the host arch
# so unit tests actually execute.
TARGET_GOOS   := darwin
TARGET_GOARCH := arm64

.PHONY: build deps tag release clean help lint test testacc
.DEFAULT_GOAL := help

include vfkit-version.sh

help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[0-9a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

$(VFKIT):
	curl -fsSL $(VFKIT_URL) -o $@
	echo "$(VFKIT_SHA256)  $@" | sha256sum -c -
	chmod +x $@

$(NETWORK_RUNNER):
	GOOS=$(TARGET_GOOS) GOARCH=$(TARGET_GOARCH) go build -ldflags "-s -w -X main.version=$(GIT_VERSION)" -o $@ ./cmd/network-runner

build: deps $(NETWORK_RUNNER) $(VFKIT) ## Build the provider binary for darwin/arm64 (embeds network-runner and vfkit)
	GOOS=$(TARGET_GOOS) GOARCH=$(TARGET_GOARCH) go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

deps: ## Tidy and vendor dependencies
	go mod tidy
	go mod vendor

tag: ## Create and push git tag — usage: make tag VERSION=1.2.3
	@test -n "$(VERSION)" || (echo "Usage: make tag VERSION=x.y.z"; exit 1)
	@if git rev-parse "v$(VERSION)" >/dev/null 2>&1; then \
		echo "Tag v$(VERSION) already exists"; exit 1; \
	fi
	git tag -a "v$(VERSION)" -m "Release v$(VERSION)"
	git push origin "v$(VERSION)"

goreleaser: ## Run goreleaser — tag must already exist; used by CI and local releases
	goreleaser release --clean

release: tag ## Push a release tag to trigger the CI release — usage: make release VERSION=1.2.3

lint: ## Run linter
	go vet ./...

test: ## Run unit tests
	go test ./...

testacc: ## Run acceptance tests (requires vfkit, network-runner, and macOS host)
	TF_ACC=1 go test -v -count=1 ./...

clean: ## Remove build artifacts
	rm -f $(BINARY) $(NETWORK_RUNNER) $(VFKIT)
