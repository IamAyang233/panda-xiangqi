# 熊猫象棋 Makefile：测试 / 构建 / 交叉编译 / 运行 / 大模型联调 / 飞牛 fpk 打包
BINARY := panda-xiangqi
VERSION := 2.0.1
# 编译期注入版本号：单文件 exe / 无 manifest 场景兜底，避免“检查更新”误报 0.0.0
LDFLAGS := -s -w -X github.com/IamAyang233/panda-xiangqi/internal/api.buildVersion=$(VERSION)

# 飞牛 fnOS 原生应用包目录与包内二进制路径（与 manifest appname 对应）。
FNK_PKG := fnos/panda-xiangqi
FNK_BIN := $(FNK_PKG)/app/server/panda-xiangqi

# 内嵌 Go 引擎的 NNUE 权重（展开格式）。由 `make nnue-prepare` 从
# dist-engines/pikafish.nnue 生成到 engines/pikafish.nnue.flat，
# fpk / dist-zip 会把它放进包的 engines/ 子目录。
#
# 引擎已完全用 Go 内嵌，因此**不再随包分发皮卡鱼二进制**，包里也没有任何
# 需要可执行位的文件 —— 这正是「皮卡鱼可用: false」（非 root 无法 chmod）
# 那个结构性问题的根除点。权重是纯数据文件，只读即可。
ENGINE_DIST := dist-engines
ENGINE_NNUE := $(ENGINE_DIST)/pikafish.nnue
ENGINE_FLAT := engines/pikafish.nnue.flat

# 默认搜索线程数上限由程序按 CPU 核数自动探测，无需在此配置。

.PHONY: all test build run clean cross dist-zip docker puzzle-check fpk fpk-arm nnue-prepare engine-match

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
	rm -rf $(FNK_PKG)/app/server/engines

## nnue-prepare: 把 pikafish.nnue 展开成定长权重，供内嵌 Go 引擎读取。
##   原文件是 zstd + COMPRESSED_LEB128 双层压缩，展开后服务端不再需要解压依赖。
##   产物 engines/pikafish.nnue.flat 约 65 MiB，已被 .gitignore 忽略，换机器需重新生成。
nnue-prepare:
	go run ./cmd/nnue-prepare -in engines/pikafish.nnue -out engines/pikafish.nnue.flat

## engine-match: 内嵌 Go 引擎与皮卡鱼的等时对拍（棋力验证）
##   两套引擎的档位参数表不通用，只有固定思考时间是可比的量，所以用 -movetime 对齐。
##   例：make engine-match ARGS="-mode moves -movetime 1000"
##       make engine-match ARGS="-mode games -movetime 600 -games 8 -maxply 100"
engine-match:
	go run ./cmd/engine-match $(ARGS)

## fpk: 交叉编译 linux/amd64 静态二进制并打包为飞牛 fnOS .fpk 原生应用
##   需先安装 fnpack（https://static2.fnnas.com/fnpack/fnpack-1.2.3-windows-amd64）。
##   包里只放展开后的权重（纯数据，无需可执行位）；权重不存在时先跑 make nnue-prepare。
fpk:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(FNK_BIN) ./cmd/server
	@if [ ! -f $(ENGINE_FLAT) ]; then \
		echo "[fpk] 错误：未找到展开后的权重 $(ENGINE_FLAT)。"; \
		echo "[fpk]       先运行 make nnue-prepare（需要原权重 $(ENGINE_NNUE)）。"; \
		exit 1; \
	fi
	rm -rf $(FNK_PKG)/app/server/engines
	mkdir -p $(FNK_PKG)/app/server/engines
	cp $(ENGINE_FLAT) $(FNK_PKG)/app/server/engines/pikafish.nnue.flat
	@if [ -f $(ENGINE_DIST)/Copying.txt ]; then \
		cp $(ENGINE_DIST)/Copying.txt $(ENGINE_DIST)/NNUE-License.md $(FNK_PKG)/app/server/engines/; \
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
	@if [ ! -f $(ENGINE_FLAT) ]; then \
		echo "[fpk-arm] 错误：未找到展开后的权重 $(ENGINE_FLAT)。"; \
		echo "[fpk-arm]       先运行 make nnue-prepare。"; \
		exit 1; \
	fi
	rm -rf $(FNK_PKG)-arm/app/server/engines
	mkdir -p $(FNK_PKG)-arm/app/server/engines
	cp $(ENGINE_FLAT) $(FNK_PKG)-arm/app/server/engines/pikafish.nnue.flat
	@if [ -f $(ENGINE_DIST)/Copying.txt ]; then \
		cp $(ENGINE_DIST)/Copying.txt $(ENGINE_DIST)/NNUE-License.md $(FNK_PKG)-arm/app/server/engines/; \
	fi
	sed -i 's/^platform=x86$$/platform=arm/' $(FNK_PKG)-arm/manifest
	@if [ -f panda-xiangqi_$(VERSION)_x86.fpk ]; then mv panda-xiangqi_$(VERSION)_x86.fpk panda-xiangqi_$(VERSION)_x86.fpk.bak; fi
	./fnpack.exe build --directory $(FNK_PKG)-arm
	mv panda-xiangqi.fpk panda-xiangqi_$(VERSION)_arm.fpk
	@if [ -f panda-xiangqi_$(VERSION)_x86.fpk.bak ]; then mv panda-xiangqi_$(VERSION)_x86.fpk.bak panda-xiangqi_$(VERSION)_x86.fpk; fi
	rm -rf $(FNK_PKG)-arm

## dist-zip: 生成 Windows 便携版（exe + 内嵌引擎权重 + 许可证 + manifest）并压缩为 zip
##   目录结构为「exe 与 engines/pikafish.nnue.flat 同级」，与运行期的权重自动探测一致；
##   放入 manifest 是为了让 exe 能读到真实版本号（否则「检查更新」会误报）。
dist-zip:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe ./cmd/server
	@if [ ! -f $(ENGINE_FLAT) ]; then \
		echo "[dist-zip] 错误：未找到展开后的权重 $(ENGINE_FLAT)。"; \
		echo "[dist-zip]       先运行 make nnue-prepare。"; \
		exit 1; \
	fi
	rm -rf dist/$(BINARY)-$(VERSION)-windows-amd64
	mkdir -p dist/$(BINARY)-$(VERSION)-windows-amd64/engines
	cp dist/$(BINARY)-windows-amd64.exe dist/$(BINARY)-$(VERSION)-windows-amd64/panda-xiangqi.exe
	cp $(ENGINE_FLAT) dist/$(BINARY)-$(VERSION)-windows-amd64/engines/pikafish.nnue.flat
	@if [ -f $(ENGINE_DIST)/Copying.txt ]; then \
		cp $(ENGINE_DIST)/Copying.txt $(ENGINE_DIST)/NNUE-License.md dist/$(BINARY)-$(VERSION)-windows-amd64/engines/; \
	fi
	cp $(FNK_PKG)/manifest dist/$(BINARY)-$(VERSION)-windows-amd64/
	cd dist && powershell -NoProfile -Command "Compress-Archive -Force -Path '$(BINARY)-$(VERSION)-windows-amd64/*' -DestinationPath '$(BINARY)_$(VERSION)_windows-amd64.zip'"
	@echo "[dist-zip] 产物：dist/$(BINARY)_$(VERSION)_windows-amd64.zip"
