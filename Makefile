BIN     := memory-indexer
CMD     := ./cmd/indexer
CONFIG  ?= config.yaml

.PHONY: build run sync deps tidy lint

build:
	go build -o $(BIN) $(CMD)

run: build
	./$(BIN) --config $(CONFIG)

sync: build
	./$(BIN) --config $(CONFIG) --sync

# Run once and exit after initial sync (useful in CI / cron)
force-sync: build
	./$(BIN) --config $(CONFIG) --sync --force

deps:
	go mod download

tidy:
	go mod tidy

lint:
	golangci-lint run ./...

install: build
	install -m 755 $(BIN) $(HOME)/.local/bin/$(BIN)
