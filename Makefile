BIN := bin
GOFLAGS := -trimpath

APP := $(BIN)/Umbra.app
MENUBAR := apps/menubar

.PHONY: build test test-integration lint clean run-daemon app app-test

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
