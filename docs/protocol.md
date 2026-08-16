# 棋境 通信协议 v1（已冻结）

坐标约定（与皮卡鱼 UCI 一致）：列 `a~i` 自红方左侧起（红方视角），行 `0~9` 自红方底线起。
格子表示为 `<列><行>`，如 `h2`。着法表示为 `from` + `to` 两个格子，如 `h2e2` = 炮二平五。

## REST

| 方法与路径 | 请求体 | 响应 |
| --- | --- | --- |
| `POST /api/games` | `{mode: "engine"\|"llm"\|"local_2p"\|"puzzle", side: "red"\|"black", level: 1~16, puzzleId?: string, llm?: LLMConfig}` | `{gameId: string, youSide: "red"\|"black"}` |
| `POST /api/games/{id}/undo` | — | `{ok: bool, reason?: string}`（思考中不可悔棋） |
| `POST /api/games/{id}/hint` | — | `{from: string, to: string, cn: string}` |
| `POST /api/games/{id}/resign` | — | `{ok: bool}` |
| `GET /api/puzzles?difficulty=入门\|初级\|中级\|高级\|大师` | — | `[{id, name, difficulty, goal, parMoves, stars?, tags}]`（不含答案） |
| `GET /api/puzzles/{id}` | — | `{id, name, difficulty, goal, parMoves, fen, tags}`（不含答案） |
| `POST /api/llm/validate` | `LLMConfig` | `{ok: bool, message: string, latencyMs: number}` |

`LLMConfig`：`{baseURL, apiKey, model, temperature?, timeoutMs?, includeLegalMoves?}`。
API Key 仅存浏览器 localStorage，服务端只做内存透传，不落盘不写日志。

## WebSocket `/api/ws?gameId=...`

客户端 → 服务端：

```json
{"type": "move", "from": "h2", "to": "e2"}
{"type": "legal", "from": "h2"}          // 查询该子合法落点（前端可落点提示）
{"type": "undo"}
{"type": "hint"}
{"type": "resign"}
{"type": "restart"}                       // 残局重开
```

服务端对 `legal` 的回应：

```json
{"type": "legal_moves", "from": "h2", "targets": ["e2", "f2", ...]}
```

服务端 → 客户端：

```json
{"type": "state", "fen": "...", "turn": "red", "lastMove": {"from": "h2", "to": "e2", "cn": "炮二平五"}, "status": "playing", "moves": ["h2e2"], "check": false, "mode": "engine", "result": null}
{"type": "engine_thinking", "side": "black"}
{"type": "engine_move", "from": "b2", "to": "e2", "cn": "炮八平五", "check": false}
{"type": "llm_move", "from": "h2", "to": "e2", "cn": "炮二平五", "comment": "中炮开局，直指中路", "check": false}
{"type": "llm_fallback", "by": "local_engine"}
{"type": "check"}                                  // 走子后对方被将军
{"type": "game_over", "result": "red_win|black_win|draw", "reason": "checkmate|stalemate|resign|repetition|60_moves|insufficient", "stars": 3}
{"type": "hint_result", "from": "h2", "to": "e2", "cn": "炮二平五"}
{"type": "undo_result", "ok": true}
{"type": "puzzle_event", "event": "deviate|step", "step": 2}   // 残局：偏离正解 / 正解推进
{"type": "error", "code": "illegal_move|not_your_turn|not_found|thinking", "message": "..."}
```

状态 `status`：`playing | over`。残局模式 `state` 额外携带 `puzzle: {id, goal, step, failed, hintUsed}`。

## 变更纪律

联调点之后如需修改本协议，必须升版本号（v2）并在此登记变更与兼容策略。
