BINARY := ask-about

# Architecture for the linux target. arm64 for Graviton, Ampere and the like.
GOARCH ?= amd64

.PHONY: help build linux dev run test fmt vet tidy clean

.DEFAULT_GOAL := help

## help: list these targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  make /'

## build: produce the self-contained binary (UI, corpus, persona embedded)
build:
	go build -trimpath -ldflags="-s -w" -o $(BINARY) ./cmd/ask-about

## linux: cross-compile for the server (make linux GOARCH=arm64 for arm)
#
# No cgo anywhere — the SQLite driver is pure Go — so this needs nothing
# installed beyond the Go toolchain and produces a binary that runs on a
# server with no libraries on it.
linux:
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -trimpath -ldflags="-s -w" -o $(BINARY)-linux-$(GOARCH) ./cmd/ask-about

## dev: run with web assets served from ./web — edit CSS/JS, refresh, no rebuild
dev:
	go run ./cmd/ask-about -dev

## run: run the embedded build
run: build
	./$(BINARY)

## test: run the test suite
test:
	go test ./...

## fmt: gofmt the tree
fmt:
	go fmt ./...

## vet: run go vet
vet:
	go vet ./...

## tidy: prune go.mod
tidy:
	go mod tidy

## clean: remove the binaries
clean:
	rm -f $(BINARY) $(BINARY)-linux-*
