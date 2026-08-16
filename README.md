# 熊猫象棋（Panda Xiangqi）

**单文件即可部署的中国象棋对战平台** —— 人机对战 · 大模型对弈 · 双人同屏 · 残局挑战。

基于《棋境项目计划书 v1.1》实现：Go 后端（单二进制、零第三方依赖）+ 原生 ES Module 前端（Canvas 分层渲染，无构建步骤），浏览器打开即玩，桌面与手机浏览器均已适配。

## 快速开始

```bash
# 构建 + 运行（默认 8080 端口，自动打开浏览器）
make run
# 或
go build -o panda-xiangqi ./cmd/server && ./panda-xiangqi

# 服务器部署（不自动开浏览器）
QIJING_OPEN_BROWSER=false QIJING_PORT=8080 ./panda-xiangqi
```

无需数据库、无需 Node、无需安装任何依赖 —— 前端、残局、音效（Web Audio 合成）、棋盘纹理（程序化绘制）全部内嵌于一个可执行文件。

## 功能

| 模式 | 说明 |
| --- | --- |
| 🤖 人机对战 | 16 档难度（入门→特大级）；可选执红/执黑；悔棋/提示/认输 |
| ✨ 大模型对弈 | 用户自配 OpenAI 兼容 API（DeepSeek/GLM/通义/Kimi/Ollama…）；每步附棋评气泡；非法输出自动重试 3 次，仍失败由本地引擎代走并在界面标注；API Key 仅存浏览器 |
| 👥 双人同屏 | 同一屏幕轮流落子；支持视角翻转 |
| 🧩 残局挑战 | 分级残局库（入门~大师）；红先胜目标 + 三星评价；偏离正解即时提示；进度存本地 |

**通用对局功能**：中文记谱（炮二平五）、着法列表、最后一手标记、将军警示（震颤 + 红光）、吃子粒子爆裂、胜利彩带、走子抛物线动画、Web Audio 合成音效、三套棋子皮肤（木/玉/瓷）+ 两套棋盘、画质自动降级（帧率不足时逐级关闭阴影与粒子）。

## 架构

```
├── cmd/server/          # 入口：静态资源内嵌、配置加载、优雅退出
├── cmd/puzzle-check/    # 残局批量校验工具（引擎自对弈验证正解并写回）
├── internal/
│   ├── game/            # 棋规引擎：16×16 mailbox、FEN、着法生成、反向攻击探测、
│   │                    #   make/unmake+Zobrist、将死/困毙/和棋、中文记谱、perft
│   ├── engine/          # Engine 接口 + SimpleEngine(α-β+迭代加深+置换表+静态搜索)
│   │                    #   + UCIEngine(皮卡鱼适配，崩溃自动重启) + Manager 档位调度
│   ├── llm/             # OpenAI 兼容客户端、JSON/中文着法解析、重试与降级链
│   ├── session/         # 对局会话状态机：四模式调度、悔棋/提示/认输、残局进度
│   ├── api/             # REST + 手写 RFC6455 WebSocket + 静态资源
│   ├── puzzle/          # 残局库（内嵌 data/*.json）+ 校验
│   └── config/          # config.yaml（极简子集）+ 环境变量覆盖
├── web/                 # 前端（原生 ES Module，go:embed 直接内嵌，无构建步骤）
│   └── js/  renderer.js # Canvas 分层渲染器（L0 木纹棋盘离屏缓存 / L1 立体棋子 / L2 特效）
│       screens/         # 大厅 / 对局 / 残局 / 设置
│       audio.js         # Web Audio 合成音效（12 种，零音频资源）
├── docs/protocol.md     # 通信协议 v1（REST + WS 消息集）
└── internal/puzzle/data # 引擎验证过的残局（正解主变入库）
```

**关键设计**（对应计划书 §3.1）：

1. **规则单一事实来源** —— 所有着法由后端 `internal/game` 校验推进，前端可落点提示通过 WS `legal` 查询获取，双端永不漂移；
2. **引擎抽象** —— `engine.Engine` 接口统一皮卡鱼（UCI 子进程）/ 自研引擎 / 大模型三种"思考者"，皮卡鱼缺失或崩溃时自动降级，开箱可玩；
3. **Key 归浏览器** —— 大模型 API Key 只存 localStorage，服务端内存透传、不落盘不写日志；
4. **全部内嵌** —— `go:embed` 打包前端与残局，交叉编译即得"整个游戏"。

## 质量保障

- **perft 基准全绿**：初始局面 d1=44 / d2=1,920 / d3=79,666 / d4=3,290,240，与 Xiangqi 标准数据逐位一致（`go test ./internal/game`）；
- **引擎测试**：一步杀识别、必吃局面、双车对光将限步必胜、局面不可变性；
- **LLM 测试**：mock 服务覆盖合法/非法重试/中文着法/超时/断网/坏配置全分支；
- **WS 端到端测试**：手写掩码客户端完成握手、入会、走子、将死、三星判定全链路（`internal/api/api_test.go`）;
- **残局 CI 复验**：全部残局正解可回放且终局为将死（`internal/puzzle/puzzle_test.go`）；新残局用 `make puzzle-check` 批量校验入库。

```bash
make test      # 全部测试
make cross     # Windows/Linux/macOS 三平台产物 → dist/
```

## 皮卡鱼（UCI 引擎，飞牛 fnOS 版随包内置）

飞牛 fnOS 版（x86 / arm）的 `panda-xiangqi-x86.fpk` / `panda-xiangqi-arm.fpk` **已随包内置皮卡鱼（Pikafish）引擎与 `pikafish.nnue` 权重**，零外部依赖、开箱即用；引擎二进制与权重位于 `app/server/engines/` 下，并由服务端在启动时自动 `chmod +x` 并指向内置权重。低档位（1~4）始终使用自研 SimpleEngine，5 档及以上优先皮卡鱼，缺失或崩溃时自动回退自研引擎。

本地源码构建 / 桌面运行（非 fnOS 包）如需皮卡鱼，可任选其一放入：

```bash
# 方式一：PATH 中
pikafish --version

# 方式二：程序同目录 engines/pikafish[.exe]

# 方式三：config.yaml
engine: /path/to/pikafish
```

> 皮卡鱼为 GPLv3。随 fnOS 包分发时，其许可证（`Copying.txt` / `NNUE-License.md`）已一并内置在 `app/server/engines/` 中；源码见官方仓库 `official-pikafish/Pikafish`。
>
> 自定义打包：把官方 release 的 `Linux/pikafish-*`（x86_64，取 `sse41-popcnt` 以兼顾老 CPU）或 `Android/pikafish-armv8`（aarch64）以及 `pikafish.nnue` 放进仓库根 `dist-engines/{x86_64,aarch64}/` 与 `dist-engines/pikafish.nnue`，再 `make fpk` / `make fpk-arm` 即自动嵌入（该目录已在 `.gitignore` 中忽略，不入库）。

## 大模型对弈配置

右上角 ⚙️ 设置 → 填写 Base URL / API Key / 模型名 → 连通性测试。示例：

| 服务商 | Base URL | 模型 |
| --- | --- | --- |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-chat` |
| 智谱 GLM | `https://open.bigmodel.cn/api/paas/v4` | `glm-4` |
| 通义千问 | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `qwen-plus` |
| 本地 Ollama | `http://localhost:11434/v1` | `qwen2.5` |

勾选"附带合法着法表"后非法率趋近于零；模型每步附一句棋评气泡。

**引擎候选增强（默认开启，推荐）**：每手先由本地引擎快速筛出按强弱排序的前 8 个候选着法，模型只需从中挑选并给出战术解说——提示词更短（响应更快）、着法全部为引擎级强手（棋力大幅提升）、解析失败时直接取首位候选（零延迟降级）。实测小模型应着从 30s+ 降至 ~19s，且着法从"自由发挥"变为规范强应（如对当头炮应以屏风马）。

**本地联调**：仓库内置 OpenAI 兼容模拟服务（`make mockllm`），无需真实 Key 即可走通大模型对弈全链路（含非法输出降级与超时降级演练）：

```bash
make mockllm                                        # http://localhost:9099/v1，模型名 mock
go run ./cmd/mockllm -mode illegal                  # 演练：非法输出 → 重试 → 本地引擎代走
go run ./cmd/mockllm -mode timeout                  # 演练：超时 → 降级
```
浏览器设置中 Base URL 填 `http://localhost:9099/v1`、模型名 `mock` 即可。

## 与计划书的差异说明

1. **前端**：计划书为 TypeScript+Vite；实际采用原生 ES Module + `go:embed` 直出，免除 Node 构建链，单二进制部署目标不变（结构按 TS 工程划分，迁移成本低）；
2. **WebSocket**：手写 RFC 6455 实现（约 300 行）替代第三方库，实现零依赖；SQLite 对局历史（可选项）暂未开启，按计划默认文件模式完整运行；
3. **残局内容**：首批 14 关（全部引擎验证，含四大杀型教学局）；四大古谱和棋排局与 50 题扩容通过 `make puzzle-check` 内容管线持续生产；
4. **长打判负**：P0 按计划仅实现三次重复判和，长打裁决列入后续版本。

## 后续路线（§13）

在线对战（WS 架构已预留）· 引擎复盘分析 · PWA/WASM 离线 · 棋谱导入导出 · 更多模型协议。
