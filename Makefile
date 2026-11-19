VERSION ?= 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build web go test test-go test-js e2e clean

build: web go

web:
	pnpm install --frozen-lockfile=false
	pnpm -r --filter "./packages/**" build
	pnpm --filter web build
	rm -rf apps/server/web/dist && mkdir -p apps/server/web/dist && cp -R apps/web/dist/. apps/server/web/dist/

go:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/rhymehub ./apps/server/cmd/rhymehub
	go build -ldflags "$(LDFLAGS)" -o bin/rhymehub-supervisor ./services/supervisor/cmd/rhymehub-supervisor

test: test-go test-js

test-go:
	go vet ./...
	go test ./...

test-js:
	pnpm -r test

clean:
	rm -rf bin out
