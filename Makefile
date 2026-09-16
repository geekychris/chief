# Chief — build/install for the daemon + CLI.
#
# Layout produced by `make build`:
#   bin/chief      -- CLI
#   bin/chiefd     -- daemon
#
# `make install` copies binaries into ~/bin and the launchd plist into
# ~/Library/LaunchAgents, then bounces chiefd so the new binary is loaded.

VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -X main.Version=$(VERSION)
BIN_DIR      := bin
# Install to ~/.local/bin — already on chris's PATH and needs no sudo.
INSTALL_DIR  := $(HOME)/.local/bin
APPS_DIR     := $(HOME)/Applications
LAUNCHAGENTS := $(HOME)/Library/LaunchAgents
PLIST        := launchd/com.chris.chiefd.plist
PLIST_LABEL  := com.chris.chiefd
MENU_PLIST       := launchd/com.chris.chief-menu.plist
MENU_PLIST_LABEL := com.chris.chief-menu
APP_BUNDLE_MENU  := Chief-Menu.app
APP_BUNDLE_UI    := Chief.app
UI_BUILT_APP     := cmd/chief-ui/build/bin/Chief.app
WAILS            := $(shell go env GOPATH)/bin/wails

.PHONY: all build build-chief build-chiefd build-chief-menu build-chief-ui app-bundle-menu app-bundle-ui install-app tidy test install install-menu install-ui install-all uninstall uninstall-menu uninstall-ui run-daemon reload reload-menu clean

all: build

build: build-chief build-chiefd build-chief-menu

build-chief:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/chief ./cmd/chief

build-chiefd:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/chiefd ./cmd/chiefd

build-chief-menu:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/chief-menu ./cmd/chief-menu

# Build the main-window Wails app (invokes `wails build` under cmd/chief-ui).
# Uses ~/go/bin/wails since GOPATH/bin isn't on Chris's PATH.
build-chief-ui:
	cd cmd/chief-ui && $(WAILS) build

# Assemble the menu-bar .app bundle. LSUIElement in Info.plist keeps it
# menu-bar-only (no Dock icon, no app-switcher).
app-bundle-menu: build-chief-menu
	rm -rf $(BIN_DIR)/$(APP_BUNDLE_MENU)
	mkdir -p $(BIN_DIR)/$(APP_BUNDLE_MENU)/Contents/MacOS
	cp $(BIN_DIR)/chief-menu $(BIN_DIR)/$(APP_BUNDLE_MENU)/Contents/MacOS/chief-menu
	cp assets/Info.plist $(BIN_DIR)/$(APP_BUNDLE_MENU)/Contents/Info.plist

# The Wails app is bundled by `wails build` itself.
app-bundle-ui: build-chief-ui

tidy:
	go mod tidy

test:
	go test ./...

# Install binaries + plist. Reloads chiefd if it's already running.
install: build
	@mkdir -p $(INSTALL_DIR) $(LAUNCHAGENTS) $(HOME)/Library/Logs/Chief
	install -m 0755 $(BIN_DIR)/chief  $(INSTALL_DIR)/chief
	install -m 0755 $(BIN_DIR)/chiefd $(INSTALL_DIR)/chiefd
	install -m 0644 $(PLIST) $(LAUNCHAGENTS)/$(PLIST_LABEL).plist
	@$(MAKE) reload

# Install the menu-bar app as Chief-Menu.app and (re)load its launchd plist.
install-menu: app-bundle-menu
	@mkdir -p $(APPS_DIR) $(LAUNCHAGENTS) $(HOME)/Library/Logs/Chief
	rm -rf $(APPS_DIR)/$(APP_BUNDLE_MENU)
	cp -R $(BIN_DIR)/$(APP_BUNDLE_MENU) $(APPS_DIR)/
	install -m 0644 $(MENU_PLIST) $(LAUNCHAGENTS)/$(MENU_PLIST_LABEL).plist
	@$(MAKE) reload-menu

# Install the main-window app as Chief.app. Also kills any running instance
# so the next launch picks up the new bundle — macOS never auto-swaps a
# Wails app when you cp -R a new build over the on-disk bundle. Manually
# quit + reopen would work too, but killing first makes the flow reliable.
install-ui: app-bundle-ui
	@mkdir -p $(APPS_DIR)
	-@pkill -f "$(APP_BUNDLE_UI)/Contents/MacOS/Chief" 2>/dev/null; sleep 0.3
	rm -rf $(APPS_DIR)/$(APP_BUNDLE_UI)
	cp -R $(UI_BUILT_APP) $(APPS_DIR)/
	@echo "Chief.app installed at $(APPS_DIR)/$(APP_BUNDLE_UI)."
	@echo "Launched a fresh instance:"
	@open -a Chief 2>/dev/null || true

# Install everything (daemon, CLI, menu bar, main window) in one shot.
install-all: install install-menu install-ui

# Kick chiefd via launchctl. Idempotent: unload silently, then load.
reload:
	-launchctl unload $(LAUNCHAGENTS)/$(PLIST_LABEL).plist 2>/dev/null
	launchctl load $(LAUNCHAGENTS)/$(PLIST_LABEL).plist
	-launchctl kickstart -k gui/$$(id -u)/$(PLIST_LABEL) 2>/dev/null
	@echo "chiefd reloaded. Try: chief ping"

reload-menu:
	-launchctl unload $(LAUNCHAGENTS)/$(MENU_PLIST_LABEL).plist 2>/dev/null
	launchctl load $(LAUNCHAGENTS)/$(MENU_PLIST_LABEL).plist
	-launchctl kickstart -k gui/$$(id -u)/$(MENU_PLIST_LABEL) 2>/dev/null
	@echo "chief-menu reloaded. Look at your menu bar for a ♦."

uninstall:
	-launchctl unload $(LAUNCHAGENTS)/$(PLIST_LABEL).plist 2>/dev/null
	-rm -f $(LAUNCHAGENTS)/$(PLIST_LABEL).plist
	-rm -f $(INSTALL_DIR)/chief $(INSTALL_DIR)/chiefd

uninstall-menu:
	-launchctl unload $(LAUNCHAGENTS)/$(MENU_PLIST_LABEL).plist 2>/dev/null
	-rm -f $(LAUNCHAGENTS)/$(MENU_PLIST_LABEL).plist
	-rm -rf $(APPS_DIR)/$(APP_BUNDLE_MENU)

uninstall-ui:
	-rm -rf $(APPS_DIR)/$(APP_BUNDLE_UI)

# Run chiefd in the foreground for interactive debugging.
run-daemon: build-chiefd
	$(BIN_DIR)/chiefd

clean:
	rm -rf $(BIN_DIR)
