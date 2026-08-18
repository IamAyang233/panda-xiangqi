# panda-xiangqi v1.1.5 — 飞牛 fnOS 测试虚拟机 安装与验证指令

> 本文档供「另一个 AI / 测试执行者」在**用户本机（Windows + Edge）**上，把 v1.1.5 装到测试 fnOS 虚拟机并验证功能之用。
> 请严格按顺序执行，遇到卡点看第 6 节「故障排查」。

---

## 0. 目标

1. 用 fnOS Web 后台在测试 VM 上安装 `panda-xiangqi_1.1.5_x86.fpk`；
2. 启动应用，打开 Web 界面；
3. 验证主界面（棋盘、谜题列表）渲染正常；
4. 进 1～2 个「杀法」谜题按正确走法走子，确认引擎判定「将死 / 胜利」且关卡标记为完成；
5. 全程截图并存盘，最终把「截图 + 文字结论」反馈给用户。

---

## 1. 环境与凭据（必读）

| 项 | 值 |
|---|---|
| 测试 VM 地址 | `http://192.168.93.128:5666/` |
| fnOS 账号 | `ja233` |
| fnOS 密码 | `Pbj781230.` ⚠️ **末尾有英文句点 `.`** |
| VM 架构 | `x86_64` → 用 **x86** 版 fpk |
| 安装包（本地） | `D:\AI项目\2026-07-21-00-18-48\panda-xiangqi\panda-xiangqi_1.1.5_x86.fpk` |
| Edge 浏览器 | `C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe` |
| agent-browser | 已安装，`agent-browser --version` 见 `0.27.0` |
| 网络代理（curl / git 用） | `http://192.168.100.254:7890` |

> 注意：测试 VM（192.168.93.128）与记忆里的生产 NAS（192.168.100.254）**不是同一台**，不要混淆。
> 该 fpk 是已发布版本，本地文件已核对存在（约 54.3 MB）。

---

## 2. 工具准备

确认前置条件（在 Git Bash / 任意 shell 中）：

```bash
node -e "console.log('node', process.version)"     # 需有 node
agent-browser --version                            # 需有 agent-browser
ls -la "/d/AI项目/2026-07-21-00-18-48/panda-xiangqi/panda-xiangqi_1.1.5_x86.fpk"  # 确认 fpk 存在
```

**若本地 fpk 缺失**（换机器执行时），从 GitHub Release 下载（走代理）：

```bash
curl -x http://192.168.100.254:7890 -L -o "/d/AI项目/2026-07-21-00-18-48/panda-xiangqi/panda-xiangqi_1.1.5_x86.fpk" \
  "https://github.com/IamAyang233/panda-xiangqi/releases/download/v1.1.5/panda-xiangqi_1.1.5_x86.fpk"
```

---

## 3. 浏览器自动化「踩坑」须知（重要，别踩）

1. **`ref` 每次页面渲染都会重新分配**。不要复用上一条命令的 `e9/e10/e4` 之类编号。每次操作前先 `agent-browser snapshot -i` 拿当前最新的 ref。
2. **`agent-browser wait --load networkidle` 在 fnOS 这种 SPA 上会卡死**（永远等不到 networkidle）。一律改用 `agent-browser wait 3000`（或 1500/5000，按页面复杂度）。
3. **登录按钮在用户名、密码都填好之前是禁用状态**，填完才会启用，再去 click。
4. fnOS 是单页应用，登录后 DOM 会整体替换，所有 ref 失效——登录后必须重新 `snapshot -i`。
5. 上传 fpk 是 `<input type="file">`，用 `agent-browser upload @ref "本地绝对路径"`（路径用 Windows 反斜杠或 Git Bash 正斜杠均可，但必须是绝对路径）。

---

## 4. 执行步骤

### 4.1 打开登录页并登录

```bash
# 用 Edge 打开测试 VM（若之前开过会话，agent-browser 会自动复用）
agent-browser open "http://192.168.93.128:5666/" \
  --executable-path "C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
```

```bash
agent-browser snapshot -i     # 拿当前 ref：找到 用户名输入框 / 密码输入框 / 登录按钮
```

假设拿到 `e9`=用户名、`e10`=密码、`e4`=登录按钮（**以你 snapshot 的实际编号为准**）：

```bash
agent-browser type @e9 "ja233"
agent-browser type @e10 "Pbj781230."      # 末尾带点
agent-browser snapshot -i                 # 确认登录按钮已启用（不再是 disabled）
agent-browser click @e4                   # 点登录
agent-browser wait 3000
agent-browser snapshot                    # 确认已进入 fnOS 桌面（出现「应用中心」「文件」等）
```

> ✅ 预期：登录后进入 fnOS 主界面，顶部/侧边有「应用中心」入口。
> 若登录失败提示密码错，先确认密码末尾的英文句点 `.` 是否漏打。

### 4.2 进入应用中心，安装 fpk

```bash
agent-browser snapshot -i     # 找到「应用中心」入口并点击（ref 以实际为准）
agent-browser wait 3000
agent-browser snapshot -i     # 在应用中心找「手动安装 / 本地安装 / 上传」按钮（通常在右上角或列表上方）
```

点击「手动安装 / 本地安装」后，会弹出文件选择（`<input type="file">`）：

```bash
agent-browser snapshot -i     # 找到 file input 的 ref（例如 e12）
agent-browser upload @e12 "D:\AI项目\2026-07-21-00-18-48\panda-xiangqi\panda-xiangqi_1.1.5_x86.fpk"
agent-browser wait 3000
agent-browser snapshot        # 确认出现「安装 / 确认安装」按钮并点击
agent-browser wait 5000       # 安装需要一点时间
agent-browser snapshot        # 确认安装完成，应用出现在「已安装」列表，状态正常
```

> ⚠️ 不同 fnOS 版本 UI 文案可能不同（如「应用中心 → 右上角 ⋯ → 手动安装」或「我的应用 → 添加」）。**以实际 snapshot 看到的按钮文字为准**，不要死磕上面假设的文案。
> 若上传/file input 找不到：可尝试 `agent-browser snapshot` 全量文本，搜索 `input` 或「选择文件」。

### 4.3 启动应用并打开 Web 界面

```bash
agent-browser snapshot -i     # 在已安装列表找到 panda-xiangqi，找「打开 / 启动」按钮
agent-browser click @<打开按钮ref>
agent-browser wait 3000
agent-browser snapshot        # 看地址栏/页面：记录应用 Web 界面的实际 URL
```

> 应用启动后通常会跳到其 Web 页面（URL 形如 `http://192.168.93.128:5666/apps/...` 或 `http://192.168.93.128:<port>/`）。**把这个 URL 记下来**，后续截图都用它。

### 4.4 界面验证（主界面 / 棋盘 / 谜题列表）

在应用 Web 界面里：

```bash
agent-browser wait 3000
agent-browser snapshot        # 记录主界面元素：是否看到棋盘、菜单、谜题入口
```

核对清单（第 5 节「验证清单」）：
- 棋盘渲染正常，「楚河 漢界」文字不溢出格子；
- 能进入「谜题 / 闯关」列表；
- 谜题总数应为 **22 关**（原 14 关 + v1.1.5 新增 8 关杀法）；
- 新增的 8 关杀法题应在列表中可见（见 4.5 的题名）。

对主界面、谜题列表各截一张图：

```bash
agent-browser screenshot "/d/AI项目/2026-07-21-00-18-48/panda-xiangqi/test_shot_01_home.png"
# 进入谜题列表后
agent-browser screenshot "/d/AI项目/2026-07-21-00-18-48/panda-xiangqi/test_shot_02_puzzlelist.png"
```

### 4.5 杀法谜题「跑关」验证（核心）

v1.1.5 新增的 8 个杀法谜题（pzl-015 ~ pzl-022），题名与杀法：

| 编号 | 题名 | 杀法类型 |
|---|---|---|
| pzl-015 | 铁门栓 | 中炮+车控将门 |
| pzl-016 | 卧槽马 | 马卧槽 |
| pzl-017 | 挂角马 | 马挂角 |
| pzl-018 | 天地炮 | 天地炮 |
| pzl-019 | 海底捞月 | 车炮海底捞月 |
| pzl-020 | 二鬼拍门 | 双车/双兵拍门 |
| pzl-021 | 双马饮泉 | 双马 |
| pzl-022 | 车兵 | 车兵 |

**验证方法（两关即可，建议 pzl-015 铁门栓 + pzl-019 海底捞月）：**

1. 在谜题列表点开该关；
2. 按界面给出的「正确走法提示」逐步走子（红先）；
3. 走完后引擎应判定 **「红方将死黑方 / 胜利」**，并标记该关「完成 / 通关」；
4. 截图记录「胜利」结果与关卡完成状态；
5. （可选）故意走一步明显错的子，确认**不会**误判胜利——验证判定逻辑没失效。

```bash
# 进关后按提示走子（走子交互依赖具体 UI，可能是点选棋子再点目标格）
# 每走一步后 wait 1000 让引擎判定
agent-browser wait 1000
agent-browser screenshot "/d/AI项目/2026-07-21-00-18-48/panda-xiangqi/test_shot_03_pzl015_win.png"
```

> 走子交互若是用鼠标点击（先点己方棋子高亮，再点目标格），用 `agent-browser click @<格ref>` 模拟；若是用坐标，先 `snapshot -i` 拿棋盘格的 ref。
> 实在无法用点击走子时，可退而求其次：只验证「该关能打开、能显示初始局面与提示文字、不报错」，并在结论里注明「走子交互未自动化，需人工确认」。

### 4.6 收尾

- 汇总所有截图路径；
- 写一段文字结论：安装是否成功、界面是否正常、杀法谜题是否可解且判定正确、有无报错。

---

## 5. 验证清单（预期结果，用于断言）

| # | 检查项 | 预期 |
|---|---|---|
| 1 | 登录 | 用 `ja233` / `Pbj781230.` 登录成功，进入 fnOS 桌面 |
| 2 | 安装 | fpk 上传后安装成功，panda-xiangqi 出现在已安装列表，无报错 |
| 3 | 启动 | 应用可启动，Web 界面可打开，记录到实际 URL |
| 4 | 棋盘 | 棋盘渲染正常，「楚河漢界」不溢出 |
| 5 | 谜题总数 | 谜题列表共 **22 关** |
| 6 | 新杀法题 | 列表含 pzl-015~022（铁门栓/卧槽马/挂角马/天地炮/海底捞月/二鬼拍门/双马饮泉/车兵） |
| 7 | 判定 | 按正确走法走完，引擎判「将死/胜利」，关卡标完成 |
| 8 | 无异常 | 全程无 JS 报错、无白屏、无崩溃 |

---

## 6. 故障排查

| 现象 | 处理 |
|---|---|
| 登录按钮一直 disabled | 确认用户名、密码都填了；密码末尾英文句点 `.` 别漏 |
| 密码错误 | 重输 `Pbj781230.`，重点检查末尾 `.` |
| `wait --load networkidle` 卡死 | 一律改用 `agent-browser wait 3000` |
| ref 点击无反应 / 点错元素 | 重新 `snapshot -i` 拿最新 ref，SPA 路由切换后旧 ref 失效 |
| 找不到「手动安装」入口 | 不同 fnOS 版本文案不同，全量 `snapshot` 搜「安装/本地/上传/手动」 |
| file input 找不到 | 全量 `snapshot` 搜 `input`；或用 `agent-browser upload @ref "绝对路径"` |
| 上传后无反应 | 确认路径是**绝对路径**；Windows 反斜杠需转义或改用 Git Bash 正斜杠 |
| 应用打不开 / 白屏 | 等久一点 `wait 5000`；查地址栏 URL 是否变了；查控制台报错 |
| 杀法题走子无法自动化 | 退而验证「能打开+显示提示+不报错」，结论注明需人工确认 |

---

## 7. 交付物（反馈给用户的内容）

1. 所有截图文件（建议命名 `test_shot_01_*.png` …，`test_shot_03_pzl015_win.png` 等）；
2. 一段文字结论，覆盖第 5 节 8 个检查项逐条「通过 / 不通过 / 未验证」；
3. 应用 Web 界面的实际 URL；
4. 任何报错信息原文。

---

## 附：关键事实速查

- 坐标系：文件 a–i（红左→右），横线 0–9（0=红方底线，9=黑方底线）；FEN 第一字段是 rank 9。
- 棋子字母：大写=红，小写=黑：K/k 将帅、A/a 士、B/E 象、N/H 马、R/r 车、C/c 炮、P/p 兵。
- 引擎判定：**将死（checkmate）与 困毙（stalemate）都算红胜**，但谜题校验只认「将死」；验证时只需确认走完判「将死/胜利」即可。
- 本安装包为已发布版本 v1.1.5，已通过 `matesolve` 8/8 与 `puzzle-check` 8/8 校验，杀法题均为真实将死。
