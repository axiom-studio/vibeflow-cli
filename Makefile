BINARY_NAME=vibeflow
CMD_DIR=./cmd/vibeflow
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

.PHONY: all build clean test vet install snapshot sync-agent-docs

all: build

build: sync-agent-docs
	go build $(LDFLAGS) -o $(BINARY_NAME) $(CMD_DIR)

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

test:
	go test ./...

vet:
	go vet ./...

install: sync-agent-docs
	go install $(LDFLAGS) $(CMD_DIR)

snapshot:
	goreleaser release --snapshot --clean

# Sync agent doc templates from the source of truth (vibecoding-agent-docs/)
# into the Go-embedded directory (internal/vibeflowcli/agentdocs/).
# CLAUDE.md gets a permissions header prepended; AGENTS.md and GEMINI.md are
# copied directly.
AGENT_DOCS_SRC=vibecoding-agent-docs
AGENT_DOCS_DST=internal/vibeflowcli/agentdocs

sync-agent-docs:
	@echo "Syncing agent docs from $(AGENT_DOCS_SRC)/ → $(AGENT_DOCS_DST)/"
	@cp $(AGENT_DOCS_SRC)/AGENTS.md $(AGENT_DOCS_DST)/AGENTS.md
	@cp $(AGENT_DOCS_SRC)/GEMINI.md $(AGENT_DOCS_DST)/GEMINI.md
	@printf '%s\n' \
		'# Claude Code Configuration' '' \
		'## Permissions' '' \
		'Allow: Bash(sleep *)' \
		'Allow: Bash(git:*)' '' \
		> $(AGENT_DOCS_DST)/CLAUDE.md
	@cat $(AGENT_DOCS_SRC)/CLAUDE.md >> $(AGENT_DOCS_DST)/CLAUDE.md
	@echo "Done. Synced CLAUDE.md, AGENTS.md, GEMINI.md."
