.PHONY: build test lint fmt vet check run preview tidy snapshot release token-store help

BIN := ocsa

build: ## build the binary into ./bin
	go build -o bin/$(BIN) .

test: ## run the test suite
	go test ./...

fmt: ## format everything
	gofmt -w .

lint: ## report formatting that would change
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi
	@echo "gofmt clean"

vet: ## static analysis
	go vet ./...

check: lint vet test ## everything CI runs

run: ## dashboard against the real install
	go run . ui

preview: ## headless render of a screen, e.g. make preview S=grouped
	go run . preview $(or $(S),dash)

tidy: ## tidy the module
	go mod tidy

snapshot: ## build release artifacts locally without publishing
	goreleaser release --snapshot --clean --skip=publish

release: ## tag and release, updating the Homebrew tap; V=1.2.3
	./scripts/release.sh $(V)

token-store: ## save the Homebrew tap token in the macOS keychain
	./scripts/release.sh --store-token

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
