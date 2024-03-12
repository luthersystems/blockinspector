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

.PHONY: static-checks
static-checks:
	./scripts/static-checks.sh
