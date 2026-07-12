BIN := bin
GOFLAGS := -trimpath

.PHONY: build test test-integration lint clean run-daemon

build:
	mkdir -p $(BIN)
	go build $(GOFLAGS) -o $(BIN)/umbrad ./cmd/umbrad
	codesign --force --entitlements build/vz.entitlements --sign - $(BIN)/umbrad
	go build $(GOFLAGS) -o $(BIN)/umbra ./cmd/umbra

test:
	go test ./... -count=1

test-integration: build
	go test -tags=integration -c -o bin/vm.test ./internal/vm
	codesign --force --entitlements build/vz.entitlements --sign - bin/vm.test
	./bin/vm.test -test.v -test.timeout 15m

lint:
	gofmt -l . && test -z "$$(gofmt -l .)"
	go vet ./...

run-daemon: build
	$(BIN)/umbrad

clean:
	rm -rf $(BIN)
