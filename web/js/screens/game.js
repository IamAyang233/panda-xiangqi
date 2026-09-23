// 对局屏：四种模式的统一交互与状态同步。
import { GameConn, createGame } from '../net.js';
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { BoardRenderer } from '../renderer.js';
import { parseSq } from '../board.js';
import { toast, showScreen, confirmDialog } from '../ui.js';
import { positionOf, nameOf } from './puzzles.js';

const $ = (id) => document.getElementById(id);

// 象棋初始满盘局面。只用于「确实需要一块干净的空白棋盘」的场景：
// 从大厅/残局列表首次进入对局时，棋盘上可能还留着上一局的残影。
const INITIAL_FEN = 'rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w';

const reasonText = {
  checkmate: '将死', stalemate: '困毙', resign: '认输',
  repetition: '三次重复局面', long_check: '长将判负', '60_moves': '六十回合未吃子', insufficient: '双方无进攻子力',
};

// 残局名来自用户输入（自摆残局可自己起名），拼进 innerHTML 前必须转义。
const escapeText = (s) => String(s).replace(/[&<>"']/g, (c) => (
  { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
));

export class GameScreen {
  constructor() {
    this.canvas = $('board-canvas');
    this.renderer = new BoardRenderer(this.canvas, {
      skin: store.theme.pieces,
      boardSkin: store.theme.board,
      onSquareClick: (f, r, name) => this._onSquare(f, r, name),
    });
    this.renderer.particles.enabled = store.theme.particles;
    this.conn = null;
    this.mode = null;
    this.humanSide = 'red';
    this.gameOver = false;
    this.selected = null;
    this.legal = [];
    this.moves = [];
    this.puzzle = null;
    this.lastPuzzleInfo = null;
    this.puzzleId = null;        // 残局模式当前关 id，供"上一关/下一关"定位相邻关
    this.onPuzzleStep = null;    // (delta:±1) => void  由 main.js 接上残局列表
    this.onExitToPuzzles = null; // 退出到残局列表后刷新列表（星级/进度）
    this._turn = 'red';          // 当前轮走方（用于提示按钮按你方禁用/恢复）

    this._bindControls();
    // 调试钩子：自动化测试与控制台排查用
    window.__qijing = { renderer: this.renderer, game: this };
  }

  _bindControls() {
    $('btn-undo').onclick = () => { sfx.play('button'); this.conn?.undo(); };
    $('btn-hint').onclick = () => {
      if ($('btn-hint').disabled || !this.conn) return;
      sfx.play('button');
      $('btn-hint').disabled = true; // 防止连点：提示请求在途期间禁用，结果/报错到达再恢复
      this.conn.hint();
    };
    $('btn-resign').onclick = async () => {
      sfx.play('button');
      if (await confirmDialog('确定认输吗？本局将判负。', { danger: true, okText: '认输' })) {
        this.conn?.resign();
      }
    };
    $('btn-restart').onclick = () => { sfx.play('button'); this.conn?.restart(); };
    $('btn-flip').onclick = () => {
      sfx.play('button');
      this.renderer.setFlipped(!this.renderer.flipped);
    };
    $('btn-exit').onclick = async () => {
      // 自摆残局没有「残局列表」这一层，它的上一层是大厅
      const back = this.mode === 'puzzle' && !this.isCustomPuzzle() ? '残局列表' : '大厅';
      if (this.conn && !this.gameOver && this.moves.length) {
        if (!(await confirmDialog(`对局进行中，确定返回${back}？`, { okText: `返回${back}` }))) return;
      }
      this.exit();
    };
    // 逐关切换：由外部（main.js）接上残局列表的相邻关
    $('btn-prev-puzzle').onclick = () => { sfx.play('button'); this.onPuzzleStep?.(-1); };
    $('btn-next-puzzle').onclick = () => { sfx.play('button'); this.onPuzzleStep?.(1); };
    $('btn-result-close').onclick = () => { $('result-overlay').hidden = true; };
    $('btn-result-exit').onclick = () => { $('result-overlay').hidden = true; this.exit(); };
    $('btn-result-restart').onclick = () => {
      $('result-overlay').hidden = true;
      if (this.mode === 'puzzle') this.conn?.restart();
      else this.start(this.mode, this.startOpts);
    };
  }

  exit() {
    this.conn?.close();
    this.conn = null;
    this._setBoardLoading(false); // 切关等待中途退出时别把遮罩留在那儿
    store.clearCurrentGame();
    // 逐层返回：内置残局对局回到残局列表（保留筛选与进度），其余回大厅。
    // 自摆残局虽然也是 puzzle 模式，但它没有「残局列表里的位置」（不参与逐关浏览），
    // 回到大厅才是它的上一层 —— 否则会被丢进内置残局列表，看着像串到别的功能里了。
    const back = this.mode === 'puzzle' && !this.isCustomPuzzle() ? 'puzzles' : 'lobby';
    showScreen(back);
    if (back === 'puzzles') this.onExitToPuzzles?.();
  }

  // start(mode, opts) 创建对局并连接。opts: {side, level, puzzleId, onExit}
  async start(mode, opts = {}) {
    // 残局模式一律不画「初始满盘」：残局从来不是满盘局面，先画满盘再换成残局
    // 就是用户看到的「先显示全部棋子，再显示残局棋子」那一闪。
    // 改为保留当前棋盘（首次进入时是空画布）并盖载入遮罩，等服务器回送 state
    // 时一次性替换；期间禁用切关按钮防连点。
    const maskBoard = mode === 'puzzle';

    this.mode = mode;
    this.startOpts = opts;
    this.gameOver = false;
    this.selected = null;
    this.legal = [];
    this.moves = [];
    this.puzzle = null;
    this.lastPuzzleInfo = null;

    const payload = { mode, side: opts.side || 'red', level: opts.level || 4 };
    if (mode === 'puzzle') payload.puzzleId = opts.puzzleId;
    this.puzzleId = mode === 'puzzle' ? (opts.puzzleId || null) : null;
    if (mode === 'llm') payload.llm = store.llm;

    let created;
    try {
      created = await createGame(payload);
    } catch (e) {
      toast(`创建对局失败：${e.message}`, true);
      return;
    }
    this.humanSide = created.youSide || 'red';
    this.renderer.setFlipped(this.humanSide === 'black');
    // 持久化当前对局：断网/刷新后可凭 gameId 重连恢复（返回大厅即清档）。
    store.saveCurrentGame({
      gameId: created.gameId,
      mode,
      youSide: this.humanSide,
      side: opts.side || 'red',
      level: opts.level || 4,
      opts,
    });
    showScreen('game');
    // 屏幕由 hidden(display:none) 切回可见后，父容器 .board-wrap 才有真实尺寸。
    // 构造函数里的 resize() 在屏幕隐藏时尺寸为 0 会提前返回（cell/mx/my 未初始化），
    // 若只依赖 ResizeObserver 在部分嵌入式 WebView 中可能不触发或延迟触发，
    // 导致棋子以 NaN/错误坐标绘制（表现为“棋子位置不对 / 不显示”）。显式重算一次几何。
    this.renderer.resize();
    if (maskBoard) {
      // 顺手清掉上一关的选中/最后一步/将军高亮，免得旧标记残留
      this.renderer.setSelected(null, []);
      this.renderer.setLastMove(null, null);
      this.renderer.checkSide = null;
      this.renderer.dirty = true;
      this._setBoardLoading(true);
    } else {
      this.renderer.setFEN(INITIAL_FEN);
    }
    this._setupUI(mode, opts);

    this.conn?.close();
    this.conn = new GameConn(created.gameId);
    this.conn.onAny((m) => this._dispatch(m));
    this.conn.onReconnecting = (n) => toast(`连接中断，正在重连…（第 ${n} 次）`, false, 2200);
    try {
      await this.conn.connect();
    } catch {
      this._setBoardLoading(false); // 连不上就别把棋盘一直盖着
      toast('连接对局服务失败', true);
      // ⚠️ 必须显式 close()：connect() 的 Promise 虽然已 reject，但 onclose 里
      // 排下的重连定时器还在跑（manualClose 仍是 false），对这个已不可能恢复的
      // gameId 会以 1s→15s 无限重试，而这里没有接 onReconnecting 之外的提示路径。
      this.conn?.close();
    }
  }

  // 棋盘载入遮罩：切关/重连期间盖住旧局面，等新 state 一次性替换。
  // 同时禁用「上一关/下一关」防止连点抢跑（旧连接会被反复关闭）。
  _setBoardLoading(on) {
    const el = $('board-loading');
    if (el) {
      el.hidden = !on;
      // 重放淡入动画：重新显示时重计延迟，否则会直接沿用上次的动画终态（立即全黑）
      if (on) { el.style.animation = 'none'; void el.offsetWidth; el.style.animation = ''; }
    }
    for (const id of ['btn-prev-puzzle', 'btn-next-puzzle']) {
      const b = $(id);
      if (b) b.disabled = !!on;
    }
  }

  // restore() 凭 localStorage 中存档的 gameId 重连恢复「进行中」的对局。
  // 服务端 Join 会回送完整 state，前端据此全量重建棋盘与着法列表。
  // 返回 true=已恢复（停在 game 屏）；false=无存档或恢复失败（调用方应显示大厅）。
  async restore() {
    const saved = store.currentGame;
    if (!saved || !saved.gameId) return false;

    this.mode = saved.mode;
    this.startOpts = saved.opts || {};
    this.gameOver = false;
    this.selected = null;
    this.legal = [];
    this.moves = [];
    this.puzzle = null;
    this.lastPuzzleInfo = null;
    // 恢复的对局同样要能逐关切换：puzzleId 存在存档的 opts 里
    this.puzzleId = this.mode === 'puzzle' ? (this.startOpts.puzzleId || null) : null;
    this.humanSide = saved.youSide || 'red';
    this.renderer.setFlipped(this.humanSide === 'black');
    showScreen('game');
    this.renderer.resize();
    // 恢复对局也不给「初始满盘」中间态（刷新后同样会一闪），直接盖遮罩等 state。
    this._setBoardLoading(true);
    this._setupUI(this.mode, this.startOpts);

    this.conn?.close();
    this.conn = new GameConn(saved.gameId);
    this.conn.onAny((m) => this._dispatch(m));
    this.conn.onReconnecting = (n) => toast(`连接中断，正在重连…（第 ${n} 次）`, false, 2200);
    this.conn.onReconnected = () => {
      // 重连成功后清空残留选中态（棋盘已随 state 全量重建）。
      this.selected = null;
      this.legal = [];
      this.renderer.setSelected(null, []);
      toast('已重新连接', false, 1500);
    };
    try {
      await this.conn.connect();
    } catch {
      // 棋局会话可能已过期（服务端保留 2 小时），视为无存档返回大厅。
      this._setBoardLoading(false);
      // ⚠️ 同 start()：不 close() 的话后台重连循环会继续对这个已失效的 gameId
      // 无限重试，而用户已经被送回大厅了。
      this.conn?.close();
      store.clearCurrentGame();
      showScreen('lobby');
      return false;
    }
    return true;
  }

  // isCustomPuzzle 自摆残局（摆局屏保存后按 id 进入，id 统一带 custom- 前缀）。
  // 它与内置残局共用 ModePuzzle 链路，但有三处不同：没有相邻关可切、没有步数正解、
  // 不评星 —— 所以界面上要区别对待。
  isCustomPuzzle() {
    return this.mode === 'puzzle' && String(this.puzzleId || '').startsWith('custom-');
  }

  _setupUI(mode, opts) {
    const isPuzzle = mode === 'puzzle';
    const isCustom = isPuzzle && String(opts.puzzleId || '').startsWith('custom-');
    $('btn-restart').hidden = !isPuzzle;
    $('btn-resign').hidden = isPuzzle;
    $('btn-hint').hidden = false;
    $('btn-flip').hidden = false;
    $('puzzle-goal').hidden = !isPuzzle;
    // 逐关切换只在「内置残局」出现：自摆残局不属于任何关卡序列，没有上一关/下一关。
    $('btn-prev-puzzle').hidden = !isPuzzle || isCustom;
    $('btn-next-puzzle').hidden = !isPuzzle || isCustom;
    // 返回目标随之变化：内置残局回列表，自摆残局回大厅（它是从大厅摆局屏来的）
    const exitLabel = isPuzzle && !isCustom ? '返回残局列表' : '返回大厅';
    $('btn-exit-label').textContent = exitLabel;
    $('btn-result-exit-label').textContent = exitLabel;
    // LLM 模式：解说条常驻（固定占位，不遮挡棋盘）；其他模式隐藏
    const bubble = $('llm-bubble');
    if (mode === 'llm') {
      bubble.hidden = false;
      bubble.classList.remove('has-comment');
      bubble.textContent = 'AI 棋手将在此解说每一手棋…';
    } else {
      bubble.hidden = true;
    }

    const names = {
      engine: ['本地引擎', `第 ${opts.level || 4} 档`],
      llm: ['大模型棋手', store.llm.model || '未命名模型'],
      local_2p: ['黑方玩家', '同屏对战'],
      // 自摆残局的守方档位就是玩家在摆局时选的那一档（内置残局才是按难度映射）。
      // 注意：这个 sub 文案当前界面并不显示（#opp-state 是「思考中」等动态状态位），
      // 守方档位改在目标栏里显示，取服务端下发的 level（权威值，见 _applyState）。
      puzzle: ['残局守方', '引擎抵抗'],
    };
    const [oppName, oppSub] = names[mode];
    const youBlack = this.humanSide === 'black';
    $('opp-name').textContent = youBlack ? (mode === 'local_2p' ? '红方玩家' : oppName) : oppName;
    $('self-name').textContent = mode === 'local_2p' ? (youBlack ? '黑方玩家' : '红方玩家')
      : (youBlack ? '黑方（你）' : '红方（你）');
    $('panel-title').textContent = {
      engine: '人机对战', llm: '大模型对弈', local_2p: '双人同屏', puzzle: '残局挑战',
    }[mode];
    // 头像颜色与方位对应（上=对方）
    const topIsRed = youBlack;
    document.querySelector('.player-bar.top .avatar').className =
      `avatar ${topIsRed ? 'red' : 'black'}`;
    document.querySelector('.player-bar.top .avatar').textContent = topIsRed ? '帅' : '将';
    document.querySelector('.player-bar.bottom .avatar').className =
      `avatar ${topIsRed ? 'black' : 'red'}`;
    document.querySelector('.player-bar.bottom .avatar').textContent = topIsRed ? '将' : '帅';
    this._renderMoves();
  }

  // ---------------------------------------------------------------- 消息分发

  _dispatch(m) {
    switch (m.type) {
      case 'state': return this._onState(m);
      case 'move': case 'engine_move': case 'llm_move': return this._onMove(m);
      case 'engine_thinking': return this._onThinking(m);
      case 'check': return this._onCheck(m);
      case 'game_over': return this._onGameOver(m);
      case 'hint_result': return this._onHint(m);
      case 'undo_result': return this._onUndo(m);
      case 'llm_comment': return this._onComment(m);
      case 'llm_fallback': return toast('本步由本地引擎代走（模型输出非法）');
      case 'puzzle_event': return this._onPuzzleEvent(m);
      case 'restart': return;
      case 'error': return this._onError(m);
      case 'pong': return;
    }
  }

  _onState(m) {
    // 悔棋动画播放中：延迟应用 state，避免 setFEN 立即覆盖动画
    if (this.renderer.anim) {
      this._pendingState = m;
      setTimeout(() => {
        if (this._pendingState === m) {
          this._pendingState = null;
          this._applyState(m);
        }
      }, 320);
      return;
    }
    this._applyState(m);
  }

  _applyState(m) {
    if (m.status === 'over') store.clearCurrentGame(); // 重连到已结束的对局：不再恢复
    this._setBoardLoading(false); // 新局面到达：撤掉载入遮罩（切关/恢复的等待到此结束）
    this._turn = m.turn;
    this.renderer.setFEN(m.fen);
    this.renderer.setLastMove(m.lastMove?.from, m.lastMove?.to);
    // 全量同步时同步将军高亮：避免悔棋/重开后“文字提示将军、棋盘却不标红”的不一致。
    this.renderer.checkSide = m.check ? m.turn : null;
    this.renderer.dirty = true;
    this.moves = m.moves || [];
    this.gameOver = m.status === 'over';
    // 服务端下发的档位（守方实际采用的难度）。自摆残局的档位显示取这里，
    // 而不是玩家提交时的 opts —— 以服务端实际生效的值为准。
    if (m.level) this.level = m.level;
    this._renderMoves();
    this._updateStates(m);
    if (m.puzzle) {
      this.puzzle = m.puzzle;
      this._renderPuzzleGoal();
    }
  }

  _updateStates(m) {
    const oppState = $('opp-state'), selfState = $('self-state');
    oppState.className = 'player-state';
    selfState.className = 'player-state';
    if (this.gameOver) {
      oppState.textContent = '对局结束';
      selfState.textContent = '';
      return;
    }
    const turnName = m.turn === 'red' ? '红方行棋' : '黑方行棋';
    const humanTurn = this.mode === 'local_2p' || m.turn === this.humanSide;
    selfState.textContent = humanTurn && !m.thinking ? '轮到你' : '';
    if (m.thinking) {
      oppState.textContent = '思考中';
      oppState.className = 'player-state thinking';
    } else {
      oppState.textContent = humanTurn ? '' : turnName;
    }
    // 将军提示
    if (m.check) {
      const side = m.turn;
      (side === this.humanSide ? selfState : oppState).textContent = '被将军！';
    }
    $('btn-undo').disabled = !!m.thinking || this.gameOver;
    $('btn-hint').disabled = this.gameOver || (this.mode !== 'local_2p' && m.turn !== this.humanSide);
    $('btn-resign').disabled = this.gameOver;
  }

  // 统一计算提示按钮可用性：进行中且轮到你时可用；引擎思考/对手回合禁用。
  _updateHintButton() {
    const turn = this._turn || this.humanSide;
    $('btn-hint').disabled = this.gameOver || (this.mode !== 'local_2p' && turn !== this.humanSide);
  }

  _onMove(m) {
    // 先探明落点是否已有子（即是否吃子）：animateMove 会立即从 board 中移除，
    // 故必须在调用前读取，否则无法据此播放“吃子”音效。
    const to = parseSq(m.to);
    const captured = !!to && this.renderer.board.has(`${to.f},${to.r}`);
    this.renderer.animateMove(m.from, m.to);
    this.renderer.setLastMove(m.from, m.to);
    this.renderer.setHint(null);
    // 双人同屏：move 消息后轮走方切换（其他模式以 state 全量同步为准，
    // 且选子判定走 humanSide，此 toggle 仅对 local_2p 有意义）。
    if (this.mode === 'local_2p') {
      this.renderer.turn = this.renderer.turn === 'red' ? 'black' : 'red';
    }
    this.moves.push({ uci: m.from + m.to, cn: m.cn });
    this._renderMoves();
    // AI 走子后轮到人类：恢复悔棋按钮与状态栏（_onThinking 曾禁用/置思考中）。
    if (this.mode !== 'local_2p' && m.byHuman === false) {
      $('btn-undo').disabled = false;
      const oppState = $('opp-state');
      oppState.className = 'player-state';
      oppState.textContent = '';
    }
    sfx.play(captured ? 'capture' : 'move');
    // 残局目标栏：走子消息自带 step，无需等 state 全量同步即可刷新"已走 N 步"。
    if (this.puzzle && m.step !== undefined) {
      this.puzzle.step = m.step;
      this._renderPuzzleGoal();
    }
  }

  _onThinking() {
    $('opp-state').textContent = '思考中';
    $('opp-state').className = 'player-state thinking';
    $('btn-undo').disabled = true;
  }

  _onCheck(m) {
    sfx.play('check');
    this.renderer.setCheck(m.side);
    const flash = $('check-flash');
    flash.classList.remove('on');
    void flash.offsetWidth; // 重启动画
    flash.classList.add('on');
  }

  _onHint(m) {
    this.renderer.setHint(m.from, m.to);
    toast(`提示：${m.cn}`, false, 5000);
    sfx.play('star');
    this._updateHintButton(); // 提示不影响轮走方，按当前你方状态恢复按钮
  }

  _onUndo(m) {
    sfx.play('button');
    // 反向动画：moves 为被撤着法（顺序=回退顺序，最后一个=最近一手）。
    // 只对最近一手播动画，其余由 state 全量同步直接覆盖。
    if (m?.moves?.length) {
      const last = m.moves[m.moves.length - 1];
      if (last?.from && last?.to) {
        this.renderer.animateUndo(last.from, last.to, last.captured);
      }
    }
  }

  _onComment(m) {
    const bubble = $('llm-bubble');
    bubble.hidden = false;
    const text = (m.comment || '').trim();
    // 空评论 = 本手模型没给棋评。此时必须复位成占位文案并去掉气泡样式：
    // 气泡内容是「替换」的，留着上一手的解说会让用户以为它在讲当前这手。
    if (!text) {
      bubble.classList.remove('has-comment');
      bubble.textContent = 'AI 棋手将在此解说每一手棋…';
      return;
    }
    bubble.classList.add('has-comment');
    bubble.textContent = text;
  }

  _onPuzzleEvent(m) {
    if (m.event === 'deviate') {
      toast(m.message || '偏离正解', true, 4000);
      sfx.play('illegal');
      return;
    }
    // 关卡自带的（或用户外置目录里的）正解着法在当前局面走不出来 —— 服务端已把它
    // 转成自由对弈并封顶星级。这里必须提示，否则玩家只会觉得「走了正解还被判错」。
    if (m.event === 'solution_broken') {
      toast(m.message || '本关正解数据有误，已转为自由对弈', true, 5000);
    }
  }

  _onError(m) {
    if (m.code === 'illegal_move' || m.code === 'thinking' || m.code === 'not_your_turn') {
      sfx.play('illegal');
    }
    toast(m.message || '操作失败', true);
    this._updateHintButton(); // 提示请求若被拒，恢复按钮可再点
  }

  _onGameOver(m) {
    this.gameOver = true;
    store.clearCurrentGame();
    const isPuzzle = this.mode === 'puzzle';
    let title, cls;
    const humanWin = m.result === (this.humanSide === 'black' ? 'black_win' : 'red_win');
    if (this.mode === 'local_2p') {
      title = m.result === 'red_win' ? '红方胜' : m.result === 'black_win' ? '黑方胜' : '和棋';
      cls = m.result === 'draw' ? 'draw' : 'win';
    } else if (isPuzzle) {
      // 通关与否由服务端判定（m.cleared），不再用「有没有 stars」反推：
      // 自摆残局不评星、不下发 stars，用 stars 判断会把胜利显示成「挑战失败」。
      if (m.cleared) { title = '通关成功'; cls = 'win'; }
      else { title = '挑战失败'; cls = 'lose'; }
    } else if (m.result === 'draw') {
      title = '和棋'; cls = 'draw';
    } else {
      title = humanWin ? '胜利' : '败北';
      cls = humanWin ? 'win' : 'lose';
    }
    $('result-title').textContent = title;
    $('result-title').className = `result-title ${cls}`;
    $('result-sub').textContent = `${reasonText[m.reason] || m.reason || ''}`;
    const stars = $('result-stars');
    if (isPuzzle && m.stars) {
      stars.hidden = false;
      stars.innerHTML = '';
      for (let i = 1; i <= 3; i++) {
        const s = document.createElement('span');
        s.className = 'star';
        s.textContent = '★';
        if (i <= m.stars) {
          setTimeout(() => { s.classList.add('lit'); sfx.play('star'); }, i * 420);
        }
        stars.appendChild(s);
      }
      if (m.stars) store.setStars(this.puzzle?.id, m.stars);
    } else {
      stars.hidden = true;
    }
    $('btn-result-restart').hidden = false;
    $('btn-result-restart').textContent = isPuzzle ? '重新挑战' : '再来一局';
    setTimeout(() => { $('result-overlay').hidden = false; }, 900);

    if (cls === 'win') {
      sfx.play('win');
      this.renderer.confetti();
      this.renderer.floatText('胜');
    } else if (cls === 'lose') {
      sfx.play('lose');
      this.renderer.floatText('负', '#e05a3a');
    } else {
      sfx.play('draw');
    }
    this._updateStates({ turn: '', thinking: false });
  }

  // ---------------------------------------------------------------- 落子交互

  async _onSquare(f, r, name) {
    if (!this.conn || this.gameOver) return;
    const key = `${f},${r}`;
    const piece = this.renderer.board.get(key);

    if (this.selected) {
      // 已选子：点击合法落点 → 走子；点己方另一子 → 换选
      if (this.legal.includes(name)) {
        const from = this.selected;
        this.renderer.setSelected(null, []);
        this.legal = [];
        this.selected = null;
        this.conn.sendMove(from.name, name);
        return;
      }
      if (piece && this._isOwnPiece(piece)) {
        this._select(f, r, name);
        return;
      }
      this.renderer.setSelected(null, []);
      this.selected = null;
      this.legal = [];
      return;
    }
    if (piece && this._isOwnPiece(piece)) this._select(f, r, name);
  }

  _isOwnPiece(piece) {
    if (this.mode === 'local_2p') return piece.color === this.renderer.turn;
    return piece.color === this.humanSide;
  }

  async _select(f, r, name) {
    this.selected = { f, r, name };
    this.renderer.setSelected({ f, r }, []);
    this.legal = await this.conn.legalTargets(name);
    if (this.selected?.name !== name) return; // 期间已切换
    this.renderer.setSelected({ f, r }, this.legal);
    if (this.legal.length) sfx.play('button');
  }

  // ---------------------------------------------------------------- 着法列表 / 残局目标

  _renderMoves() {
    const list = $('move-list');
    list.innerHTML = '';
    for (let i = 0; i < this.moves.length; i += 2) {
      const row = document.createElement('div');
      row.className = 'mv-row';
      const no = document.createElement('span');
      no.className = 'mv-no';
      no.textContent = (i / 2 + 1) + '.';
      const red = document.createElement('span');
      red.className = 'mv-red';
      red.textContent = this.moves[i]?.cn || '';
      const black = document.createElement('span');
      black.className = 'mv-black';
      black.textContent = this.moves[i + 1]?.cn || '';
      row.append(no, red, black);
      list.appendChild(row);
    }
    list.scrollTop = list.scrollHeight;
  }

  _renderPuzzleGoal() {
    const el = $('puzzle-goal');
    if (!this.puzzle) { el.hidden = true; return; }
    const side = this.puzzle.playerSide === 'black' ? '黑先' : '红先';
    const aim = this.puzzle.goal === 'win' ? '胜' : '和';
    const goal = `${side}${aim}`;
    const failed = this.puzzle.failed ? '<b style="color:#e05a3a">（已偏离正解，可悔棋或重开）</b>' : '';
    el.hidden = false;

    // 自摆残局：没有正解、没有步数标准、不评星，因此不显示「第 N/M 关」「最少 N 步」
    // 和星级说明 —— 那些数字对自摆局面没有意义，摆出来只会让人以为被评了星。
    if (this.isCustomPuzzle()) {
      const title = this.puzzle.name || nameOf(this.puzzleId);
      // 显示守方实际档位：玩家在摆局屏用滑块选的难度要能在这里得到确认
      el.innerHTML = `${title ? `<div class="puzzle-pos">${escapeText(title)}</div>` : ''}目标：<b>${goal}</b> · 守方 <b>第 ${this.level || 4} 档</b> · 已走 <b>${this.puzzle.step}</b> 步${failed}
        <br>自摆局面不评星，练手为主`;
      return;
    }

    // 逐关浏览需要知道"我在第几关"：显示 第 N/M 关 + 关名
    const pos = this.puzzleId ? positionOf(this.puzzleId) : null;
    const title = this.puzzleId ? nameOf(this.puzzleId) : '';
    const head = pos
      ? `<div class="puzzle-pos">第 <b>${pos.index}</b>/<b>${pos.total}</b> 关${title ? ` · ${title}` : ''}</div>`
      : '';
    el.innerHTML = `${head}目标：<b>${goal}</b> · 最少 <b>${this.puzzle.parMoves}</b> 步 · 已走 <b>${this.puzzle.step}</b> 步${failed}
      <br>不用提示且步数达标 = ★★★`;
  }
}
