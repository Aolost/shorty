GO ?= go
BINARY := shorty

.PHONY: all build test vet fmt fuzz check clean

all: build

build:
	$(GO) build -trimpath -o $(BINARY) ./cmd/shorty

test:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:" && gofmt -l . && exit 1)

fuzz:
	$(GO) test -fuzz=FuzzValidateURL -fuzztime=5s ./internal/urlval
	$(GO) test -fuzz=FuzzRouting -fuzztime=5s ./internal/server

check: fmt vet test build

clean:
	rm -f $(BINARY)
