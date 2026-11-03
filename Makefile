BIN := bin/orch

.PHONY: build test check docs-check install link

build:
	go build -o $(BIN) .

test:
	go test ./...

check: build docs-check
	go vet ./...
	go test ./...
	./$(BIN) playbook check playbooks

link: build
	mkdir -p $(HOME)/.local/bin
	ln -sf $(CURDIR)/$(BIN) $(HOME)/.local/bin/orch

install: check link
	$(HOME)/.local/bin/orch install --repo $(CURDIR)

# Regenerate in a temporary tree so the check never modifies tracked documents.
docs-check: build
	@set -eu; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	for file in README.md docs/setup.md skill/SKILL.md skill/references/design.md constitution/orch-block.md; do \
		mkdir -p "$$tmp/$$(dirname "$$file")"; cp "$$file" "$$tmp/$$file"; \
	done; \
	./$(BIN) docs gen --root "$$tmp"; \
	for file in README.md docs/setup.md skill/SKILL.md skill/references/design.md constitution/orch-block.md; do \
		diff -u "$$file" "$$tmp/$$file"; \
	done
