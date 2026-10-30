GO ?= go

.PHONY: all build data update run test clean

all: build

build:
	$(GO) build -trimpath -o bin/builder ./cmd/builder
	$(GO) build -trimpath -o bin/server ./cmd/server

# 离线构建：只用 data/raw 里的本地副本
data:
	./bin/builder -config config/sources.json -out data/dist -basedir .

# 联网更新规则源后再构建
update:
	./bin/builder -prepare always -config config/sources.json -out data/dist -basedir .

run:
	./bin/server -addr :8080 -dir data/dist

# 只扫本项目代码，避开 .tools 里的 Go SDK 源码
test:
	$(GO) vet ./cmd/... ./internal/...
	$(GO) test ./cmd/... ./internal/...
	@test -z "$$(gofmt -l cmd internal)" || (echo "以下文件未 gofmt:"; gofmt -l cmd internal; exit 1)

clean:
	rm -rf bin data/dist
