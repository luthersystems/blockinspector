BIN=./build/blockinspector
GO_FILES=$(shell find . -name '*.go')

.PHONY: default
default: build
	@

.PHONY: build
build: ${BIN}
	@

${BIN}: ${GO_FILES}
	mkdir -p $(dir $@)
	go build -o $@ ./cmd/blockinspector

.PHONY: clean
clean:
	rm -f ${BIN}

.PHONY: test

.PHONY: citest
citest: go-test

.PHONY: go-test
test: go-test
go-test:
	go test -cover ./...

.PHONY: install
install:
	go install ./cmd/blockinspector

# explorer serves the block explorer API for a local fabric-network-builder
# network, over mutual TLS. NETWORK_DIR is the directory that holds the
# network's crypto-config/ (a project's fabric/ directory). For example:
#   make explorer NETWORK_DIR=../ui-core/fabric
NETWORK_DIR ?=
CHANNEL ?= luther
PEER_ADDRESS ?= 127.0.0.1:7051
EXPLORER_LISTEN ?= 127.0.0.1:8090

.PHONY: explorer
explorer: ${BIN}
	@test -n "${NETWORK_DIR}" || { echo "Set NETWORK_DIR to the network's directory (the one with crypto-config/)." >&2; exit 1; }
	${BIN} serve --network-dir "${NETWORK_DIR}" --channel "${CHANNEL}" --peer-address "${PEER_ADDRESS}" --listen "${EXPLORER_LISTEN}"
