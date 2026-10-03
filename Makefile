# Glasshouse task runner.
#
#   make test          run Go tests (vet + fmt check included)
#   make run-mock      start the Inspector on the mock adapter
#   make stack-up     start Postgres 17 and the Inspector in containers (builds from source)
#   make stack-down   stop the stack and delete its data
#   make run-postgres start the Inspector on the host instead (stop the stack first: same port)
#   make demo         before/after snapshot around an insert (needs the stack or run-postgres)
#
# Go runs on the host by default. If Go is not installed, use Docker:
#   make test USE_DOCKER=1

INSPECTOR_DIR := inspector
PG_DIR        := adapters/postgres
ADDR          ?= 127.0.0.1:8765
ORIGIN        ?= https://article.example
PG_DSN        ?= postgres://glasshouse_inspector:glasshouse-local-inspector@127.0.0.1:55432/glasshouse?sslmode=disable
BIN_DIR       := $(INSPECTOR_DIR)/bin
BIN           := $(BIN_DIR)/inspector

USE_DOCKER ?= 0
GO_IMAGE   ?= golang:1.24-alpine

ifeq ($(USE_DOCKER),1)
  # Run the Go toolchain in a container, mounting the repo so output lands here.
  GO    = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -e GOPATH=/tmp/gopath -e GOCACHE=/tmp/gocache -e CGO_ENABLED=0 -v $(CURDIR)/$(INSPECTOR_DIR):/src -w /src $(GO_IMAGE) go
  GOFMT = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/$(INSPECTOR_DIR):/src -w /src $(GO_IMAGE) gofmt
else
  GO    = go
  GOFMT = gofmt
endif

.PHONY: help test vet fmt-check fmt build run-mock run-postgres stack-up stack-down stack-logs demo clean

help:
	@grep -E '^#   make ' Makefile | sed 's/^#   //'

# ---- Go ---------------------------------------------------------------------

fmt-check:
	@out=$$(cd $(INSPECTOR_DIR) && $(GOFMT) -l .); \
	 if [ -n "$$out" ]; then echo "gofmt needed (run make fmt):"; echo "$$out"; exit 1; fi

fmt:
	cd $(INSPECTOR_DIR) && $(GOFMT) -w .

vet:
	cd $(INSPECTOR_DIR) && $(GO) vet ./...

test: fmt-check vet
	cd $(INSPECTOR_DIR) && $(GO) test -count=1 ./...

build:
	mkdir -p $(BIN_DIR)
	cd $(INSPECTOR_DIR) && CGO_ENABLED=0 $(GO) build -o bin/inspector ./cmd/inspector
	@echo "built $(BIN)"

# ---- run --------------------------------------------------------------------

run-mock: build
	$(BIN) -adapter mock -addr $(ADDR) -origin $(ORIGIN)

run-postgres: build
	GLASSHOUSE_PG_DSN='$(PG_DSN)' $(BIN) -adapter postgres -addr $(ADDR) -origin $(ORIGIN)

# ---- Postgres ---------------------------------------------------------------

# The compose file builds the Inspector from this checkout.
stack-up:
	cd $(PG_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) docker compose -f compose.yml up -d --build --wait
	@echo "viewer: http://127.0.0.1:8765/"

stack-down:
	cd $(PG_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) docker compose -f compose.yml down -v

stack-logs:
	cd $(PG_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) docker compose -f compose.yml logs --tail=50

demo:
	sh $(PG_DIR)/demo.sh

# ---- misc -------------------------------------------------------------------

clean:
	rm -rf $(BIN_DIR)
