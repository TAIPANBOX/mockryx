VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
STATICCHECK ?= staticcheck

.PHONY: build test vet fmt lint staticcheck run clean

build:
	go build $(LDFLAGS) -o bin/mockryx ./cmd/mockryx

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint: vet staticcheck
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

# Static analysis beyond go vet. Install: go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck:
	@command -v $(STATICCHECK) >/dev/null 2>&1 && $(STATICCHECK) ./... || echo "staticcheck not installed; skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"

# Rehearse the shipped example scenarios against a gateway you already have
# running (set MOCKRYX_GATEWAY, or pass --gateway). This is a fire drill
# against infrastructure you own; it never targets anything else.
run: build
	./bin/mockryx run ./scenarios --gateway "$${MOCKRYX_GATEWAY:-http://127.0.0.1:8080}"

clean:
	rm -rf bin
