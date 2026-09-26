BIN := bin/orch

.PHONY: build test check install link

build:
	go build -o $(BIN) .

test:
	go test ./...

check: build
	go vet ./...
	go test ./...
	./$(BIN) playbook check playbooks

link: build
	mkdir -p $(HOME)/.local/bin
	ln -sf $(CURDIR)/$(BIN) $(HOME)/.local/bin/orch

install: check link
	$(HOME)/.local/bin/orch install --repo $(CURDIR)
