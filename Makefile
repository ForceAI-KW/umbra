BIN := bin
GOFLAGS := -trimpath
VERSION := $(shell cat VERSION)

APP := $(BIN)/Umbra.app
MENUBAR := apps/menubar
RELEASE_TARBALL := $(BIN)/umbra-$(VERSION)-macos-arm64.tar.gz

.PHONY: build test test-integration lint clean run-daemon app app-test release install uninstall

build:
	mkdir -p $(BIN)
	go build $(GOFLAGS) -o $(BIN)/umbrad ./cmd/umbrad
	codesign --force --entitlements build/vz.entitlements --sign - $(BIN)/umbrad
	go build $(GOFLAGS) -o $(BIN)/umbra ./cmd/umbra

# app assembles the SwiftUI menu bar app into a real .app bundle (bundling the
# just-built umbra CLI into Contents/MacOS so it's found regardless of PATH),
# ad-hoc signed like umbrad. Requires the Swift toolchain (Xcode CLT).
app: build
	swift build -c release --package-path $(MENUBAR)
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp $(MENUBAR)/.build/release/UmbraMenuBar $(APP)/Contents/MacOS/UmbraMenuBar
	cp $(BIN)/umbra $(APP)/Contents/MacOS/umbra
	cp $(MENUBAR)/Resources/Info.plist $(APP)/Contents/Info.plist
	codesign --force --deep --sign - $(APP)

app-test:
	swift test --package-path $(MENUBAR)

# install builds everything, then runs scripts/install.sh to put umbra/umbrad
# on PATH, Umbra.app in /Applications, and load the LaunchAgent. Override the
# locations with UMBRA_BIN_DIR / UMBRA_APP_DIR.
install:
	./scripts/install.sh

uninstall:
	./scripts/uninstall.sh

# release assembles the signed binaries + app bundle from build/app into
# bin/umbra-<version>-macos-arm64.tar.gz (clean paths inside the tarball via
# `tar -C`, no bin/ prefix). Version comes from the VERSION file.
release: build app
	rm -rf $(BIN)/release-stage
	mkdir -p $(BIN)/release-stage
	cp $(BIN)/umbrad $(BIN)/umbra $(BIN)/release-stage/
	cp -R $(APP) $(BIN)/release-stage/Umbra.app
	cp LICENSE $(BIN)/release-stage/
	cp scripts/install.sh scripts/uninstall.sh $(BIN)/release-stage/
	chmod +x $(BIN)/release-stage/install.sh $(BIN)/release-stage/uninstall.sh
	printf 'Umbra %s -- macOS arm64\n===============================\n\n' "$(VERSION)" > $(BIN)/release-stage/INSTALL.txt
	printf 'Requirements: macOS 13+ on Apple Silicon (arm64).\n\n' >> $(BIN)/release-stage/INSTALL.txt
	printf 'ONE-SHOT INSTALL (recommended):\n     ./install.sh\n\n' >> $(BIN)/release-stage/INSTALL.txt
	printf 'This puts umbra + umbrad on your PATH, Umbra.app in /Applications,\nand loads the umbrad LaunchAgent (auto-start at login). Uninstall: ./uninstall.sh\n\n' >> $(BIN)/release-stage/INSTALL.txt
	printf 'MANUAL (if you prefer):\n  1. sudo cp umbra umbrad /usr/local/bin/\n  2. umbra daemon install\n  3. open Umbra.app\n\n' >> $(BIN)/release-stage/INSTALL.txt
	printf 'First-run note: umbrad ships ad-hoc codesigned with the\ncom.apple.security.virtualization entitlement; macOS shows an interactive,\none-time permission prompt the first time it boots a VM -- approve it to\nallow Virtualization.framework access.\n\n' >> $(BIN)/release-stage/INSTALL.txt
	printf 'Docs: https://github.com/ForceAI-KW/umbra\nTroubleshooting: docs/PITFALLS-EXTERNAL.md, docs/runbooks/\n' >> $(BIN)/release-stage/INSTALL.txt
	tar -czf $(RELEASE_TARBALL) -C $(BIN)/release-stage umbrad umbra Umbra.app LICENSE INSTALL.txt install.sh uninstall.sh
	rm -rf $(BIN)/release-stage
	@echo "release: $(RELEASE_TARBALL)"

test:
	go test ./... -count=1

test-integration: build
	go test -tags=integration -c -o $(BIN)/vm.test ./internal/vm
	codesign --force --entitlements build/vz.entitlements --sign - $(BIN)/vm.test
	./$(BIN)/vm.test -test.v -test.timeout 15m

lint:
	gofmt -l . && test -z "$$(gofmt -l .)"
	go vet ./...

run-daemon: build
	$(BIN)/umbrad

clean:
	rm -rf $(BIN) $(MENUBAR)/.build
