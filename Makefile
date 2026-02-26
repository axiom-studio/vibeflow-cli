BINARY_NAME=vibeflow
CMD_DIR=./cmd/vibeflow
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: all build clean test vet install snapshot

all: build

build:
	go build $(LDFLAGS) -o $(BINARY_NAME) $(CMD_DIR)

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

test:
	go test ./...

vet:
	go vet ./...

install:
	go install $(LDFLAGS) $(CMD_DIR)

snapshot:
	goreleaser release --snapshot --clean
