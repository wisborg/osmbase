# osmbase -- the standard way to work with this code.
#
# One command, `osmbase`, and a library behind it. Every target here is a plain
# `go` invocation rather than a delegation to a script: unlike fitdash, which
# keeps its jobs in scripts/fd so that each is one string an allowlist can
# approve, this repository's jobs are short enough that a script would be a
# second spelling of them and nothing else.
#
# `make` on its own lists the targets.

BIN     := osmbase
PKG     := ./cmd/osmbase
GOFILES := $(shell find . -name '*.go' -not -path './.scratch/*')

# FUZZTIME bounds `make fuzz`. The default is long enough to be worth running
# and short enough to sit in a coffee break; CI would want more.
FUZZTIME ?= 30s

# ARGS passes flags straight through, e.g.
#   make run ARGS="render --lat -33.86 --lon 151.21 --out map.png"
ARGS ?=

.DEFAULT_GOAL := help

## help: list these targets
help:
	@echo "osmbase targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | awk -F': ' '{printf "  %-12s %s\n", $$1, $$2}'

## build: compile ./osmbase
build: $(BIN)

$(BIN): $(GOFILES) go.mod
	go build -o $(BIN) $(PKG)

## install: put osmbase on your PATH, in GOBIN
install:
	go install $(PKG)

## run: build and run, e.g. make run ARGS="inspect"
run: build
	./$(BIN) $(ARGS)

## gates: gofmt, vet and test -- what must pass before a commit
gates:
	@printf 'gofmt: '
	@out=$$(gofmt -l $(shell go list -f '{{.Dir}}' ./...)); \
	  if [ -n "$$out" ]; then echo "FAILED"; echo "$$out"; exit 1; else echo "clean"; fi
	@printf 'vet:   '
	@if go vet ./... >/dev/null 2>&1; then echo "clean"; else echo "FAILED"; go vet ./...; exit 1; fi
	@printf 'test:  '
	@if go test ./... >/dev/null 2>&1; then echo "all packages ok"; else echo "FAILED"; go test ./...; exit 1; fi

## test: run the tests, or narrow with PKG= and RUN=
test:
	go test $(if $(PKG),$(PKG),./...) $(if $(RUN),-run $(RUN),) -count=1

## race: the tests under the race detector
race:
	go test -race ./... -count=1

## cover: test coverage per package
cover:
	go test ./... -cover

# The two packages that parse untrusted binary input are the module's whole
# attack surface, which is why their fuzz targets are checked in rather than
# run once and forgotten. They assert invariants -- entries ascending, counts
# bounded by input length, every ring wound the way its role requires -- not
# merely the absence of a crash.
## fuzz: run every fuzz target for FUZZTIME each (default 30s)
fuzz:
	@for t in FuzzParseHeader FuzzDecodeDirectory FuzzReadArchive; do \
	  printf '%-20s ' $$t; \
	  go test ./pmtiles/ -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) 2>&1 | grep -E 'elapsed|FAIL' | tail -1; \
	done
	@printf '%-20s ' FuzzDecode
	@go test ./mvt/ -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime $(FUZZTIME) 2>&1 | grep -E 'elapsed|FAIL' | tail -1

## tidy: go mod tidy, then show what the module requires
tidy:
	go mod tidy
	@echo "--- requires ---"
	@go list -m all | tail -n +2

# The LIBRARY admits two non-standard-library modules, by name: x/image, and
# x/text beneath it. That is a deliberate constraint rather than an accident
# of scope: this library is depended on by public Apache-2.0 programs whose
# NOTICE files are maintained by hand, and every module a library package
# compiles in arrives in all of them.
#
# The COMMAND admits one more, github.com/wisborg/output, for its progress
# bars, with the two modules that package needs: go-runewidth and uax29. It
# is admitted for cmd/ only, and checked as such: a library package that
# began importing it would fail here. The programs built on this library
# already depend on output themselves, which is why the command may.
#
# Asked of the DIRECT and indirect requires and of what is actually compiled,
# not of the whole module graph, which lists modules nothing here reaches.
## deps: check nothing but the admitted modules crept in, and output only into cmd/
##
## A module beyond these is still a failure here, and a visible edit to this
## file, to NOTICE and to docs/architecture.md.
LIBRARY_MODULES := golang.org/x/image golang.org/x/text
COMMAND_MODULES := github.com/wisborg/output github.com/mattn/go-runewidth github.com/clipperhouse/uax29
deps:
	@admitted='^github.com/wisborg/osmbase$$'; for m in $(LIBRARY_MODULES) $(COMMAND_MODULES); do admitted="$$admitted|^$$m(/v[0-9]+)?$$"; done; \
	  bad=$$(go mod edit -json | sed -n 's/.*"Path": "\(.*\)".*/\1/p' | grep -Ev "$$admitted" || true); \
	  if [ -n "$$bad" ]; then echo "deps: FAILED -- go.mod requires modules not admitted:"; echo "$$bad"; exit 1; fi
	@lib=$$(go list ./... | grep -v '/cmd/'); \
	  admitted='^github.com/wisborg/osmbase'; for m in $(LIBRARY_MODULES); do admitted="$$admitted|^$$m"; done; \
	  bad=$$(go list -deps $$lib | grep -E '^[a-z0-9-]+\.[a-z]+/' | grep -Ev "$$admitted" || true); \
	  if [ -n "$$bad" ]; then echo "deps: FAILED -- these non-stdlib packages are compiled into the library:"; echo "$$bad"; exit 1; fi
	@admitted='^github.com/wisborg/osmbase'; for m in $(LIBRARY_MODULES) $(COMMAND_MODULES); do admitted="$$admitted|^$$m"; done; \
	  bad=$$(go list -deps ./cmd/... | grep -E '^[a-z0-9-]+\.[a-z]+/' | grep -Ev "$$admitted" || true); \
	  if [ -n "$$bad" ]; then echo "deps: FAILED -- these non-stdlib packages are compiled into the command:"; echo "$$bad"; exit 1; fi
	@echo "deps: the library compiles in x/image and x/text only; the command adds output, go-runewidth and uax29"

## clean: remove the binary and empty .scratch/
clean:
	rm -f $(BIN)
	@find .scratch -mindepth 1 -maxdepth 1 ! -name env -exec rm -rf {} + 2>/dev/null || true
	@echo "cleaned"

.PHONY: help build install run gates test race cover fuzz tidy deps clean
