# 熊猫象棋 通信协议 v2

坐标约定（与 UCI 一致）：列 `a~i` 自红方左侧起（红方视角），行 `0~9` 自红方底线起。
格子表示为 `<列><行>`，如 `h2`。着法表示为 `from` + `to` 两个格子，如 `h2e2` = 炮二平五。

## REST

| 方法与路径 | 请求体 | 响应 |
| --- | --- | --- |
| `POST /api/games` | `{mode: "engine"\|"llm"\|"local_2p"\|"puzzle", side: "red"\|"black", level: 1~16, puzzleId?: string, llm?: LLMConfig}` | `{gameId: string, youSide: "red"\|"black"}` |
| `POST /api/games/{id}/undo` | — | `{ok: bool, reason?: string}`（思考中不可悔棋） |
| `POST /api/games/{id}/hint` | — | `{from: string, to: string, cn: string}` |
| `POST /api/games/{id}/resign` | — | `{ok: bool}` |
| `POST /api/games/{id}/restart` | — | `{ok: bool}`；残局重开（回到该关初始局面）。未知操作 404、会话状态冲突 409 |
| `GET /api/status` | — | `{app, version, engine, uciAvailable, engineDiag, puzzles, customPuzzles, customDir, customWritable, sessions, gateway*, goVersion, platform, timestamp}` |
| `GET /api/puzzles?difficulty=入门\|初级\|中级\|高级\|大师\|自定义` | — | `[{id, name, source?, difficulty, playerSide, goal, firstSide, parMoves, tags?}]`（不含答案） |
| `GET /api/puzzles/{id}` | — | `{id, name, difficulty, playerSide, goal, firstSide, parMoves, tags?}`（不含答案） |
| `POST /api/puzzles` | `{name?, fen, side: "red"\|"black", goal: "win"\|"draw"}` | `{id, name, difficulty, playerSide, goal, firstSide, parMoves}`；保存一个**自摆残局** |
| `POST /api/puzzles/{id}/delete` | — | `{ok: true}`；仅可删除自定义残局 |
| `POST /api/llm/validate` | `LLMConfig` | `{ok: bool, message: string, latencyMs: number}` |
| `GET /api/update` | — | 转发 PanDa 更新服务，并附 `selfVersion`（本机版本，供前端比对） |
| `POST /api/feedback` | `{category, title, desc, contact?, logs?}` | `{ok: bool, message: string}`（转发 PanDa 反馈系统） |

> `/api/status` 的 `customDir` / `customWritable` 是**自定义残局降级为内存态的唯一对外通道**：
> 目录不可写时 `customWritable=false`，此时保存只活在进程内存里，重启即丢。

`LLMConfig`：`{baseURL, apiKey, model, temperature?, timeoutMs?, includeLegalMoves?}`。
API Key 仅存浏览器 localStorage，服务端只做内存透传，不落盘不写日志。

### 自定义残局（v2 新增）

- **落盘位置**：独立目录（配置项 `custom` / `custom_dir` / `custom_puzzles` 等别名，或环境变量
  `QIJING_CUSTOM`）。默认位置按「显式配置 ＞ 框架目录 ＞ 相对路径」解析：飞牛上未配置时为
  `${TRIM_PKGVAR}/custom-puzzles`（数据卷内，升级替换应用目录不会带走），本地运行为 `./custom-puzzles`。
  与内置题库目录 `puzzles` **刻意分开** —— 后者是「整体替换内嵌题库」，写进去会让用户
  必须配该目录、否则 3576 关消失。
- **全服共享**：存在服务端，任何客户端 `GET /api/puzzles` 都能看到，id 前缀统一为 `custom-`。
- **四条硬校验**（任一不过 → 400，带具体原因）：FEN 可解析；双方各恰好一枚将/帅；
  局面合法（九宫 / 士象位置 / 将帅不照面 / 非行棋方不被将军）；先走方至少有一个合法着法。
  客户端也会预检并提示，但**服务端是唯一防线**。
- **起局**：与内置残局同一入口 —— `POST /api/games` 带 `mode:"puzzle"` 与自定义 `puzzleId` 即可。
- **删除**：`POST /api/puzzles/{id}/delete`。删除用 POST 而非 `DELETE`，与其它变更动作一致，
  也不依赖网关放行 `DELETE`。内置残局 id 一律 403 拒绝；目录不可写（内存态）时返回 400 说明原因。
- **难度字段**：固定为 `"自定义"`，`parMoves` 恒为 `0`（无正解步数，故不评星）；
  守方引擎档位取玩家开局时传入的 `level`。

## WebSocket `/api/ws?gameId=...`

客户端 → 服务端：

```json
{"type": "move", "from": "h2", "to": "e2"}
{"type": "legal", "from": "h2"}          // 查询该子合法落点（前端可落点提示）
{"type": "undo"}
{"type": "hint"}
{"type": "resign"}
{"type": "restart"}                       // 残局重开
{"type": "ping"}                          // 心跳（经网关反代时防空闲被掐断）
```

服务端对 `legal` 的回应：

```json
{"type": "legal_moves", "from": "h2", "targets": ["e2", "f2", ...]}
```

服务端 → 客户端：

```json
{"type": "state", "fen": "...", "turn": "red", "lastMove": {"from": "h2", "to": "e2", "cn": "炮二平五"}, "status": "playing", "moves": [{"uci": "h2e2", "cn": "炮二平五", "red": true}], "check": false, "mode": "engine", "result": null, "reason": "", "level": 4, "thinking": false}
{"type": "engine_thinking", "side": "black"}
{"type": "engine_move", "from": "b2", "to": "e2", "cn": "炮八平五", "check": false, "byHuman": false, "step": 1}
{"type": "llm_move", "from": "h2", "to": "e2", "cn": "炮二平五", "check": false}
{"type": "llm_comment", "comment": "中炮开局，直指中路"}    // 与 llm_move 分开的两条消息；空串表示本手无棋评
{"type": "llm_fallback", "by": "local_engine"}
{"type": "check", "side": "black"}                         // side = 被将军的一方
{"type": "game_over", "result": "red_win|black_win|draw", "reason": "checkmate|stalemate|resign|repetition|long_check|60_moves|insufficient", "cleared": true, "stars": 3}
{"type": "hint_result", "from": "h2", "to": "e2", "cn": "炮二平五"}
{"type": "undo_result", "ok": true, "moves": [{"from": "h2", "to": "e2", "cn": "炮二平五", "captured": "p"}]}
{"type": "puzzle_event", "event": "deviate|solution_broken", "message": "..."}
{"type": "error", "code": "illegal_move|not_your_turn|not_found|thinking|bad_message|unknown_type", "message": "..."}
{"type": "error", "code": "legal|undo|hint|resign|restart", "message": "..."}   // 该操作自身失败时回显操作名
{"type": "pong"}
```

状态 `status`：`playing | over`。

残局模式（`mode == "puzzle"`）的 `state` 额外携带：

```json
"puzzle": {"id": "custom-...", "name": "自摆残局 09-22 18:40", "goal": "win", "playerSide": "red", "firstSide": "red", "step": 0, "failed": false, "hintUsed": false, "parMoves": 0}
```

`playerSide` 与 `firstSide` 是**两个独立维度**：

- `playerSide`：玩家执哪一方（用户在建局时选）；
- `firstSide`：起始局面的轮走方（由 FEN 决定，服务端从局面解析得出）。

两者可以不同，共四种组合 —— 「红先·我执红」与「黑先·我执黑」是"玩家先走"，
「红先·我执黑」与「黑先·我执红」是"对手（AI）先走"（摆中局练防守反击、或复现
"黑方守和"这类残局；内置题库里也有一关是执黑而红先）。因此**界面显示「红先/黑先」
必须用 `firstSide`**，用 `playerSide` 反推会在这两种局上标反。

`game_over.cleared`（v2 新增）：残局模式下的「是否通关」，**显式下发**。
前端不再用「有没有 `stars`」反推通关 —— 自摆残局不评星（无 `stars`），
用 `stars` 判断会把胜利显示成「挑战失败」。`stars` 只在 `parMoves > 0` 的内置残局里出现。

## 变更纪律

联调点之后如需修改本协议，必须升版本号并在此登记变更与兼容策略。

## 修订记录

- **v2**（自定义残局）：新增 `POST /api/puzzles`、`POST /api/puzzles/{id}/delete`；
  `difficulty` 增加 `自定义`；`state.puzzle` 增加 `name`；`game_over` 增加 `cleared`；
  `state.moves` 元素是对象（`{uci, cn, red}`）而 v1 文档误记为字符串数组，此处一并订正。
  兼容性：纯增量，旧客户端不受影响（不认识 `cleared` 时仍可用 `stars` 判断内置残局通关）。
- **v1**：初始冻结。
