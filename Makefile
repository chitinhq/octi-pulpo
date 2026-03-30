.PHONY: all build clean install wire-mcp octi-pulpo octi-worker octi-timer

INSTALL_DIR ?= $(HOME)/.agentguard/bin

all: build

build: octi-pulpo octi-worker octi-timer

octi-pulpo:
	go build -o bin/octi-pulpo ./cmd/octi-pulpo/

octi-worker:
	go build -o bin/octi-worker ./cmd/octi-worker/

octi-timer:
	go build -o bin/octi-timer ./cmd/octi-timer/

install: build
	mkdir -p $(INSTALL_DIR)
	cp bin/octi-pulpo $(INSTALL_DIR)/octi-pulpo
	cp bin/octi-worker $(INSTALL_DIR)/octi-worker
	cp bin/octi-timer $(INSTALL_DIR)/octi-timer
	@echo "Installed to $(INSTALL_DIR)"

wire-mcp: install
	bash scripts/wire-mcp.sh

clean:
	rm -f bin/octi-pulpo bin/octi-worker bin/octi-timer
