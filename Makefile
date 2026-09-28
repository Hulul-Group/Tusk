# Tusk — build, test and run.
#
#   make web      build the React SPA (web/dist) and stage it for go:embed
#   make build    web + static Go binaries into bin/
#   make test     go vet + race tests + web typecheck/lint
#   make docker   build the tusk:dev image
#   make run      build and run the all-in-one binary (sqlite, :3000)

GO        ?= go
NPM       ?= npm
IMAGE     ?= tusk:dev
BIN       := bin
CMDS      := tusk redirector api worker
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
GOFLAGS_B := -trimpath -ldflags '$(LDFLAGS)'
EMBED_DIR := internal/webui/dist

export CGO_ENABLED ?= 0

.PHONY: all web web-deps web-embed build go-build test go-test web-test vet lint docker run clean

all: build

web-deps:
	cd web && $(NPM) ci --no-audit --no-fund

web: web-deps
	cd web && $(NPM) run build
	$(MAKE) web-embed

# Copy web/dist into the embed directory, keeping the committed .gitignore.
web-embed:
	find $(EMBED_DIR) -mindepth 1 ! -name .gitignore -exec rm -rf {} +
	cp -R web/dist/. $(EMBED_DIR)/

build: web go-build

go-build:
	@mkdir -p $(BIN)
	@for c in $(CMDS); do \
		if [ -d cmd/$$c ]; then \
			echo "go build cmd/$$c"; \
			$(GO) build $(GOFLAGS_B) -o $(BIN)/$$c ./cmd/$$c || exit 1; \
		else echo "skip cmd/$$c (not present)"; fi; \
	done

vet:
	$(GO) vet ./...

go-test:
	$(GO) test -race -count=1 ./...

web-test:
	cd web && $(NPM) run typecheck && $(NPM) run lint

test: vet go-test web-test

lint: vet web-test

docker:
	docker build -t $(IMAGE) .

run: build
	./$(BIN)/tusk

clean:
	rm -rf $(BIN) web/dist
	find $(EMBED_DIR) -mindepth 1 ! -name .gitignore -exec rm -rf {} +
