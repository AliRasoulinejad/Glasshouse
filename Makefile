# Glasshouse task runner.
#
#   make test          run Go tests (vet + fmt check included)
#   make run-mock      start the Inspector on the mock adapter
#   make stack-up     start Postgres + Inspector (origin defaults to the article's)
#   make stack-down   stop the stack and delete its data
#   make stack-restart   stack-down then stack-up, e.g. to pick up code changes
#   make run-postgres start the Inspector on the host instead (stop the stack first: same port)
#   make demo         before/after snapshot around an insert (needs the stack or run-postgres)
#   make site-build   build the static articles into site/dist (validates lab actions)
#   make web-capture  re-capture the hosted articles' step snapshots from a fresh stack
#   make web-build    build the hosted (Next.js) site into web/out
#   make web-test     typecheck and test the hosted site
#   make gateway-integration   start and remove one real lab through Docker
#   make deploy-up    build and start the hosted stack (deploy/.env), load lab images
#   make deploy-down  stop the hosted stack (labs die with it)
#
# Go runs on the host by default. If Go is not installed, use Docker:
#   make test USE_DOCKER=1

INSPECTOR_DIR := inspector
PG_DIR        := adapters/postgres
SITE_DIR      := site
GATEWAY_DIR   := gateway
ADDR          ?= 127.0.0.1:8765
ORIGIN        ?= https://article.example
PG_DSN        ?= postgres://glasshouse_inspector:glasshouse-local-inspector@127.0.0.1:55432/glasshouse?sslmode=disable
BIN_DIR       := $(INSPECTOR_DIR)/bin
BIN           := $(BIN_DIR)/inspector

USE_DOCKER ?= 0
GO_IMAGE   ?= golang:1.24-alpine

ifeq ($(USE_DOCKER),1)
  # Run the Go toolchain in a container, mounting the repo so output lands here.
  GO        = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -e GOPATH=/tmp/gopath -e GOCACHE=/tmp/gocache -e CGO_ENABLED=0 -v $(CURDIR)/$(INSPECTOR_DIR):/src -w /src $(GO_IMAGE) go
  GOFMT     = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/$(INSPECTOR_DIR):/src -w /src $(GO_IMAGE) gofmt
  # Mount site/ instead of inspector/: the site module is a separate Go
  # module, so it needs its own container mount.
  GO_SITE   = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -e GOPATH=/tmp/gopath -e GOCACHE=/tmp/gocache -e CGO_ENABLED=0 -v $(CURDIR)/$(SITE_DIR):/src -w /src $(GO_IMAGE) go
  GOFMT_SITE = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/$(SITE_DIR):/src -w /src $(GO_IMAGE) gofmt
  GO_GW     = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -e GOPATH=/tmp/gopath -e GOCACHE=/tmp/gocache -e CGO_ENABLED=0 -v $(CURDIR)/$(GATEWAY_DIR):/src -w /src $(GO_IMAGE) go
  GOFMT_GW  = docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR)/$(GATEWAY_DIR):/src -w /src $(GO_IMAGE) gofmt
else
  GO        = go
  GOFMT     = gofmt
  GO_SITE   = go
  GOFMT_SITE = gofmt
  GO_GW     = go
  GOFMT_GW  = gofmt
endif

.PHONY: help test vet fmt-check fmt build run-mock run-postgres stack-up stack-down stack-restart stack-logs demo site-actions site-build site-fmt-check site-vet web-deps web-capture web-build web-test gateway-fmt-check gateway-vet gateway-integration inspector-image deploy-up deploy-images deploy-down clean

help:
	@grep -E '^#   make ' Makefile | sed 's/^#   //'

# ---- Go ---------------------------------------------------------------------

fmt-check:
	@out=$$(cd $(INSPECTOR_DIR) && $(GOFMT) -l .); \
	 if [ -n "$$out" ]; then echo "gofmt needed (run make fmt):"; echo "$$out"; exit 1; fi

fmt:
	cd $(INSPECTOR_DIR) && $(GOFMT) -w .
	cd $(SITE_DIR) && $(GOFMT_SITE) -w .
	cd $(GATEWAY_DIR) && $(GOFMT_GW) -w .

vet:
	cd $(INSPECTOR_DIR) && $(GO) vet ./...

# The site module holds the HTML-sanitization guarantee (article.Build), so
# it gets the same fmt/vet/test gate as the Inspector, not an ad-hoc check.
site-fmt-check:
	@out=$$(cd $(SITE_DIR) && $(GOFMT_SITE) -l .); \
	 if [ -n "$$out" ]; then echo "gofmt needed in site/ (run make fmt):"; echo "$$out"; exit 1; fi

site-vet:
	cd $(SITE_DIR) && $(GO_SITE) vet ./...

gateway-fmt-check:
	@out=$$(cd $(GATEWAY_DIR) && $(GOFMT_GW) -l .); \
	 if [ -n "$$out" ]; then echo "gofmt needed in gateway/:"; echo "$$out"; exit 1; fi

gateway-vet:
	cd $(GATEWAY_DIR) && $(GO_GW) vet ./...

test: fmt-check vet site-fmt-check site-vet gateway-fmt-check gateway-vet
	cd $(INSPECTOR_DIR) && $(GO) test -count=1 ./...
	cd $(SITE_DIR) && $(GO_SITE) test -count=1 ./...
	cd $(GATEWAY_DIR) && $(GO_GW) test -count=1 ./...

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
	cd $(PG_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) GLASSHOUSE_ALLOWED_ORIGIN=$${GLASSHOUSE_ALLOWED_ORIGIN:-unused} docker compose -f compose.yml down -v

# down -v then up --build, so a fresh volume and a rebuilt image pick up
# changes to the init script or the Inspector's code.
stack-restart: stack-down stack-up

stack-logs:
	cd $(PG_DIR) && GLASSHOUSE_INSPECTOR_CONTEXT=../../$(INSPECTOR_DIR) GLASSHOUSE_ALLOWED_ORIGIN=$${GLASSHOUSE_ALLOWED_ORIGIN:-unused} docker compose -f compose.yml logs --tail=50

demo:
	sh $(PG_DIR)/demo.sh

# ---- site -------------------------------------------------------------------

site-actions:
	cd $(INSPECTOR_DIR) && $(GO) run ./cmd/inspector -list-actions -adapter postgres > ../site/actions.txt

site-build: site-actions
	cd $(SITE_DIR) && $(GO_SITE) run ./cmd/build -articles articles -out dist -actions actions.txt
	@echo "site built in site/dist"

# ---- hosted site (web/) ------------------------------------------------------

WEB_DIR := web

web-deps:
	cd $(WEB_DIR) && npm ci

# Restarts the local stack on a fresh volume once per article; stop any
# other path on :8765 first.
web-capture: site-actions
	cd $(WEB_DIR) && node scripts/capture.mjs

web-build: site-actions
	cd $(WEB_DIR) && npx next build
	@echo "hosted site built in web/out"

web-test: site-actions
	cd $(WEB_DIR) && npx tsc --noEmit && node --test scripts/

# ---- hosted gateway (gateway/, deploy/) -------------------------------------

LAB_IMAGES := glasshouse-inspector:latest postgres:17-alpine
DEPLOY     := docker compose -f deploy/compose.yml --env-file deploy/.env

inspector-image:
	docker build -t glasshouse-inspector:latest $(INSPECTOR_DIR)

# Runs in a container on the host network with the Docker socket, the way
# the gateway shares dind's network namespace in production.
gateway-integration: inspector-image
	docker pull -q postgres:17-alpine
	docker run --rm --network host -v /var/run/docker.sock:/var/run/docker.sock \
	  -v $(CURDIR)/$(GATEWAY_DIR):/src -v $(CURDIR)/$(PG_DIR)/compose.yml:/lab/compose.yml:ro \
	  -v $(CURDIR)/$(GATEWAY_DIR)/lab.override.yml:/lab/lab.override.yml:ro \
	  -e GLASSHOUSE_LAB_DIR=/lab -e CGO_ENABLED=0 -w /src $(GO_IMAGE) \
	  sh -c 'apk add -q docker-cli docker-cli-compose && go test -count=1 -run Docker -v ./internal/lab/'

# Lab starts never pull: the images are loaded into dind here.
deploy-images: inspector-image
	docker pull -q postgres:17-alpine
	docker save $(LAB_IMAGES) | $(DEPLOY) exec -T dind docker -H unix:///run/dind/docker.sock load

deploy-up: web-build
	$(DEPLOY) up -d --build
	$(MAKE) deploy-images
	$(DEPLOY) restart gateway

deploy-down:
	$(DEPLOY) down

# ---- misc -------------------------------------------------------------------

clean:
	rm -rf $(BIN_DIR)
