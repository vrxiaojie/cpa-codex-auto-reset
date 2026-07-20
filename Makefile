PLUGIN := cpa-codex-auto-reset
VERSION := 0.1.3
GOOS := $(shell go env GOOS)

ifeq ($(GOOS),darwin)
EXT := dylib
else ifeq ($(GOOS),windows)
EXT := dll
else
EXT := so
endif

.PHONY: fmt vet test race build clean

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	mkdir -p build
	CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -buildvcs=false -ldflags="-s -w -X github.com/vrxiaojie/cpa-codex-auto-reset/internal/plugin.Version=$(VERSION)" -o build/$(PLUGIN).$(EXT) .
	rm -f build/$(PLUGIN).h

clean:
	rm -rf build dist
