# 熊猫象棋 Makefile：测试 / 构建 / 交叉编译 / 运行 / 大模型联调 / 飞牛 fpk 打包
BINARY := panda-xiangqi
VERSION := 1.1.0
LDFLAGS := -s -w

# 飞牛 fnOS 原生应用包目录与包内二进制路径（与 manifest appname 对应）。
FNK_PKG := fnos/panda-xiangqi
FNK_BIN := $(FNK_PKG)/app/server/panda-xiangqi

.PHONY: all test build run clean cross docker puzzle-check fpk

all: build

## test: 运行全部测试（含 perft 基准与残局复验）
test:
	go test ./...

## build: 构建本平台单二进制（前端与残局已内嵌）
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/server

## run: 本地运行（默认 8080，自动打开浏览器）
run: build
	./$(BINARY)

## cross: 三平台交叉编译到 dist/
cross:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe ./cmd/server
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 ./cmd/server
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 ./cmd/server

## puzzle-check: 批量校验/重生成残局正解（引擎自对弈）
puzzle-check:
	go run ./cmd/puzzle-check -in internal/puzzle/data/puzzles.json
	go run ./cmd/puzzle-check -in internal/puzzle/data/more.json

## mockllm: 启动 OpenAI 兼容模拟服务（大模式对战联调，端口 9099）
mockllm:
	go run ./cmd/mockllm -addr :9099

clean:
	rm -f $(BINARY) server.log
	rm -rf dist

## fpk: 交叉编译 linux/amd64 静态二进制并打包为飞牛 fnOS .fpk 原生应用
##   需先安装 fnpack（https://static2.fnnas.com/fnpack/fnpack-1.2.3-windows-amd64）。
fpk:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(FNK_BIN) ./cmd/server
	fnpack build --directory $(FNK_PKG)
