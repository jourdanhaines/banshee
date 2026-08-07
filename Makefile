# banshee — GTK4 layer-shell launcher + tmux session manager
#
#   make build      build ./bin/banshee
#   make install    build, then install binary, shell plugins, unit and example plugin
#   make test       go test ./...
#   make lint       gofmt check + go vet (+ golangci-lint when installed)
#   make warm       pre-build the gotk4 cgo dependency tree (slow, once)
#   make uninstall  remove everything install put in place (configs are kept)

SHELL := /bin/sh

BIN         := banshee
BIN_DIR     := bin
BIN_PATH    := $(BIN_DIR)/$(BIN)
PKG         := ./cmd/banshee

PREFIX      ?= $(HOME)/.local
BINDIR      ?= $(PREFIX)/bin
SHAREDIR    ?= $(PREFIX)/share/banshee
XDG_CONFIG_HOME ?= $(HOME)/.config
CONFIGDIR   ?= $(XDG_CONFIG_HOME)/banshee
SYSTEMDDIR  ?= $(XDG_CONFIG_HOME)/systemd/user

VERSION     := $(shell sed -n 's/^const Version = "\(.*\)"$$/\1/p' internal/config/config.go)

# CGo is mandatory: GTK4 and gtk4-layer-shell are C libraries.
export CGO_ENABLED = 1

GO          ?= go
GOFLAGS     ?=
# -s -w strips the symbol table and DWARF: ~30% smaller binary, and banshee is
# debugged from source, not from core dumps.
LDFLAGS     ?= -s -w

.DEFAULT_GOAL := build
.PHONY: all build deps warm test test-race lint fmt vet smoke install install-bin \
        install-shell install-service install-config install-plugins uninstall clean help

all: lint test build

## deps — resolve and download the module graph.
deps:
	$(GO) mod download

## warm — pre-compile the gotk4 cgo tree so later builds are fast.
## Cold, this takes 5–15 minutes. It only has to happen once per machine.
warm: deps
	$(GO) build $(GOFLAGS) ./...

## build — produce ./bin/banshee.
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_PATH) $(PKG)
	@echo "built $(BIN_PATH) (banshee $(VERSION))"

## test — the full unit suite. No display, tmux server or network required.
test:
	$(GO) test ./...

## test-race — the same suite under the race detector.
## internal/theme is excluded: -race implies -d=checkptr, and gotk4 v0.4.0's
## weak-reference helper violates it inside GTK's toggle-notify callback, so any
## test that constructs a real GObject (theme's CSS round-trip is the only one
## outside the gtksmoke tag) aborts the run. Upstream bug, not banshee's;
## `make test` still covers that package.
RACE_PKGS = $$($(GO) list ./... | grep -v '/internal/theme$$')
test-race:
	$(GO) test -race $(RACE_PKGS)

## smoke — the GTK smoke test. Needs a live Wayland/X session; not part of `test`.
smoke:
	$(GO) test -tags gtksmoke -run Smoke ./internal/daemon

## fmt — rewrite every file with gofmt.
fmt:
	gofmt -w .

## vet — go vet over everything.
vet:
	$(GO) vet ./...

## lint — fail on unformatted files, then vet, then golangci-lint if present.
lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed — skipping (go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)"; \
	fi

install: build install-bin install-shell install-service install-config install-plugins
	@echo
	@echo "banshee $(VERSION) installed."
	@echo "  binary       $(BINDIR)/$(BIN)"
	@echo "  shell plugin $(SHAREDIR)/banshee.plugin.{zsh,bash}"
	@echo "  systemd unit $(SYSTEMDDIR)/banshee.service (optional)"
	@echo
	@echo "Next: run 'banshee doctor', and add the Hyprland snippet it prints."

install-bin:
	@mkdir -p $(BINDIR)
	install -m 0755 $(BIN_PATH) $(BINDIR)/$(BIN)

install-shell:
	@mkdir -p $(SHAREDIR)
	install -m 0644 shell/banshee.plugin.zsh  $(SHAREDIR)/banshee.plugin.zsh
	install -m 0644 shell/banshee.plugin.bash $(SHAREDIR)/banshee.plugin.bash

install-service:
	@mkdir -p $(SYSTEMDDIR)
	install -m 0644 contrib/banshee.service $(SYSTEMDDIR)/banshee.service

## install-config — never clobbers an existing banshee.conf.
install-config:
	@mkdir -p $(CONFIGDIR)/sessions $(CONFIGDIR)/groups $(CONFIGDIR)/plugins
	@if [ -f $(CONFIGDIR)/banshee.conf ]; then \
		echo "keeping existing $(CONFIGDIR)/banshee.conf"; \
	else \
		install -m 0644 contrib/banshee.conf $(CONFIGDIR)/banshee.conf; \
		echo "installed $(CONFIGDIR)/banshee.conf"; \
	fi

## install-plugins — the example exec plugin, only when it is not already there.
install-plugins:
	@if [ -d $(CONFIGDIR)/plugins/example ]; then \
		echo "keeping existing $(CONFIGDIR)/plugins/example"; \
	else \
		mkdir -p $(CONFIGDIR)/plugins/example; \
		install -m 0644 plugins/example/manifest.json $(CONFIGDIR)/plugins/example/manifest.json; \
		install -m 0755 plugins/example/plugin.sh     $(CONFIGDIR)/plugins/example/plugin.sh; \
		echo "installed example plugin to $(CONFIGDIR)/plugins/example"; \
	fi

uninstall:
	-$(BINDIR)/$(BIN) quit 2>/dev/null || true
	rm -f  $(BINDIR)/$(BIN)
	rm -f  $(SHAREDIR)/banshee.plugin.zsh $(SHAREDIR)/banshee.plugin.bash
	rm -f  $(SYSTEMDDIR)/banshee.service
	-rmdir $(SHAREDIR) 2>/dev/null || true
	@echo "banshee removed. Configs in $(CONFIGDIR) were left in place."

clean:
	rm -rf $(BIN_DIR)

help:
	@grep -E '^## ' Makefile | sed 's/^## /  /'
