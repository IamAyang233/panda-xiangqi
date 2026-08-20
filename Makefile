# 熊猫象棋 Makefile：测试 / 构建 / 交叉编译 / 运行 / 大模型联调 / 飞牛 fpk 打包
BINARY := panda-xiangqi
VERSION := 1.2.6
# 编译期注入版本号：单文件 exe / 无 manifest 场景兜底，避免“检查更新”误报 0.0.0
LDFLAGS := -s -w -X github.com/IamAyang233/panda-xiangqi/internal/api.buildVersion=$(VERSION)

# 飞牛 fnOS 原生应用包目录与包内二进制路径（与 manifest appname 对应）。
FNK_PKG := fnos/panda-xiangqi
FNK_BIN := $(FNK_PKG)/app/server/panda-xiangqi

# 内置皮卡鱼引擎（可选）：把官方 release 的二进制与 pikafish.nnue 放入 dist-engines/
# 后，fpk / fpk-arm 会将其嵌入 app/server/engines/。该目录已在 .gitignore 中忽略（不入库）。
ENGINE_DIST := dist-engines
ENGINE_NNUE := $(ENGINE_DIST)/pikafish.nnue
ENGINE_X86  := $(ENGINE_DIST)/x86_64/pikafish
ENGINE_ARM  := $(ENGINE_DIST)/aarch64/pikafish

.PHONY: all test build run clean cross docker puzzle-check fpk fpk-arm

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
##   若 dist-engines/ 下存在皮卡鱼二进制与 pikafish.nnue，则随包内置。
fpk:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(FNK_BIN) ./cmd/server
	@if [ -f $(ENGINE_X86) ] && [ -f $(ENGINE_NNUE) ]; then \
		mkdir -p $(FNK_PKG)/app/server/engines; \
		cp $(ENGINE_X86) $(FNK_PKG)/app/server/engines/pikafish; \
		cp $(ENGINE_NNUE) $(FNK_PKG)/app/server/engines/pikafish.nnue; \
		cp $(ENGINE_DIST)/Copying.txt $(FNK_PKG)/app/server/engines/Copying.txt; \
		cp $(ENGINE_DIST)/NNUE-License.md $(FNK_PKG)/app/server/engines/NNUE-License.md; \
		echo "[fpk] 内置皮卡鱼(x86_64) 已嵌入"; \
	else \
		echo "[fpk] 未找到 dist-engines，跳过内置引擎"; \
	fi
	./fnpack.exe build --directory $(FNK_PKG)
	mv panda-xiangqi.fpk panda-xiangqi_$(VERSION)_x86.fpk

## fpk-arm: 交叉编译 linux/arm64 并打包 ARM 版 .fpk（自动复制目录、改 platform、产物重命名）
##   临时复制 fnos/panda-xiangqi-arm 目录并把 manifest 的 platform=x86 改为 arm，打包后
##   重命名为 panda-xiangqi-arm.fpk，最后清理临时目录。会自动保护已存在的 x86 版
##   panda-xiangqi-x86.fpk 不被覆盖。
fpk-arm:
	rm -rf $(FNK_PKG)-arm
	cp -r $(FNK_PKG) $(FNK_PKG)-arm
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(FNK_PKG)-arm/app/server/panda-xiangqi ./cmd/server
	@if [ -f $(ENGINE_ARM) ] && [ -f $(ENGINE_NNUE) ]; then \
		mkdir -p $(FNK_PKG)-arm/app/server/engines; \
		cp $(ENGINE_ARM) $(FNK_PKG)-arm/app/server/engines/pikafish; \
		cp $(ENGINE_NNUE) $(FNK_PKG)-arm/app/server/engines/pikafish.nnue; \
		cp $(ENGINE_DIST)/Copying.txt $(FNK_PKG)-arm/app/server/engines/Copying.txt; \
		cp $(ENGINE_DIST)/NNUE-License.md $(FNK_PKG)-arm/app/server/engines/NNUE-License.md; \
		echo "[fpk-arm] 内置皮卡鱼(aarch64) 已嵌入"; \
	else \
		echo "[fpk-arm] 未找到 dist-engines，跳过内置引擎"; \
	fi
	sed -i 's/^platform=x86$$/platform=arm/' $(FNK_PKG)-arm/manifest
	@if [ -f panda-xiangqi_$(VERSION)_x86.fpk ]; then mv panda-xiangqi_$(VERSION)_x86.fpk panda-xiangqi_$(VERSION)_x86.fpk.bak; fi
	./fnpack.exe build --directory $(FNK_PKG)-arm
	mv panda-xiangqi.fpk panda-xiangqi_$(VERSION)_arm.fpk
	@if [ -f panda-xiangqi_$(VERSION)_x86.fpk.bak ]; then mv panda-xiangqi_$(VERSION)_x86.fpk.bak panda-xiangqi_$(VERSION)_x86.fpk; fi
	rm -rf $(FNK_PKG)-arm
