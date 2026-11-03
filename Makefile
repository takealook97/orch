BIN := bin/orch

.PHONY: build test check docs-check install link herdr

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

# Validate every destination before copying any files. Shipped names are reserved.
herdr:
	@set -eu; \
	for mod in herdr/mods/*; do \
		[ -d "$$mod" ] || continue; \
		name=$${mod##*/}; \
		case "$$name" in spaces|agents|tabs|menu|prompt|toast|help|confirm|files|changes|peek|picker|pane|worktree|workspace-chrome|workspace-status|new-menu|terminals-bar|terminals-pane|win98|machine-add) \
			echo "Refusing to overwrite shipped Herdr mod: $$name" >&2; exit 1;; esac; \
	done; \
	for layout in herdr/layouts/*.json; do \
		[ -f "$$layout" ] || continue; \
		name=$${layout##*/}; \
		case "$$name" in main.json|terminals.json|win98.json) \
			echo "Refusing to overwrite shipped Herdr layout: $$name" >&2; exit 1;; esac; \
	done; \
	mkdir -p "$(HOME)/.config/herdr-app/mods" "$(HOME)/.config/herdr-app/layouts"; \
	for mod in herdr/mods/*; do \
		[ -d "$$mod" ] || continue; \
		name=$${mod##*/}; \
		mkdir -p "$(HOME)/.config/herdr-app/mods/$$name"; \
		rsync -a --delete --exclude='*.test.mjs' "$$mod/" "$(HOME)/.config/herdr-app/mods/$$name/"; \
	done; \
	for layout in herdr/layouts/*.json; do \
		[ -f "$$layout" ] || continue; \
		cp "$$layout" "$(HOME)/.config/herdr-app/layouts/"; \
	done

# Regenerate in a temporary tree so the check never modifies tracked documents.
docs-check: build
	@set -eu; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	for file in README.md docs/setup.md skill/SKILL.md skill/references/design.md constitution/orch-block.md; do \
		mkdir -p "$$tmp/$$(dirname "$$file")"; cp "$$file" "$$tmp/$$file"; \
	done; \
	env -u ORCH_ROLE ./$(BIN) docs gen --root "$$tmp"; \
	for file in README.md docs/setup.md skill/SKILL.md skill/references/design.md constitution/orch-block.md; do \
		diff -u "$$file" "$$tmp/$$file"; \
	done
