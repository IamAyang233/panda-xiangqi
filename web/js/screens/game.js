// 对局屏：四种模式的统一交互与状态同步。
import { GameConn, createGame } from '../net.js';
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { BoardRenderer } from '../renderer.js';
import { parseSq } from '../board.js';
import { toast, showScreen, confirmDialog } from '../ui.js';

const $ = (id) => document.getElementById(id);

const reasonText = {
  checkmate: '将死', stalemate: '困毙', resign: '认输',
  repetition: '三次重复局面', '60_moves': '六十回合未吃子', insufficient: '双方无进攻子力',
};

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

    this._bindControls();
    // 调试钩子：自动化测试与控制台排查用
    window.__qijing = { renderer: this.renderer, game: this };
  }

  _bindControls() {
    $('btn-undo').onclick = () => { sfx.play('button'); this.conn?.undo(); };
    $('btn-hint').onclick = () => { sfx.play('button'); this.conn?.hint(); };
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
      if (this.conn && !this.gameOver && this.moves.length) {
        if (!(await confirmDialog('对局进行中，确定返回大厅？', { okText: '返回大厅' }))) return;
      }
      this.exit();
    };
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
    showScreen('lobby');
  }

  // start(mode, opts) 创建对局并连接。opts: {side, level, puzzleId, onExit}
  async start(mode, opts = {}) {
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
    this.renderer.setFEN('rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w');
    showScreen('game');
    this._setupUI(mode, opts);

    this.conn?.close();
    this.conn = new GameConn(created.gameId);
    this.conn.onAny((m) => this._dispatch(m));
    try {
      await this.conn.connect();
    } catch {
      toast('连接对局服务失败', true);
    }
  }

  _setupUI(mode, opts) {
    const isPuzzle = mode === 'puzzle';
    $('btn-restart').hidden = !isPuzzle;
    $('btn-resign').hidden = isPuzzle;
    $('btn-hint').hidden = false;
    $('btn-flip').hidden = false;
    $('puzzle-goal').hidden = !isPuzzle;
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
    this.renderer.setFEN(m.fen);
    this.renderer.setLastMove(m.lastMove?.from, m.lastMove?.to);
    this.moves = m.moves || [];
    this.gameOver = m.status === 'over';
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

  _onMove(m) {
    this.renderer.animateMove(m.from, m.to);
    this.renderer.setLastMove(m.from, m.to);
    this.renderer.setHint(null);
    // move 消息后轮走方切换（state 仅在加入/悔棋时全量下发）
    this.renderer.turn = this.renderer.turn === 'red' ? 'black' : 'red';
    this.moves.push({ uci: m.from + m.to, cn: m.cn });
    this._renderMoves();
    sfx.play('capture');
    setTimeout(() => sfx.play('move'), 60);
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
  }

  _onUndo() { sfx.play('button'); }

  _onComment(m) {
    const bubble = $('llm-bubble');
    bubble.hidden = false;
    bubble.classList.add('has-comment');
    bubble.textContent = m.comment || '';
  }

  _onPuzzleEvent(m) {
    if (m.event === 'deviate') {
      toast(m.message || '偏离正解', true, 4000);
      sfx.play('illegal');
    }
  }

  _onError(m) {
    if (m.code === 'illegal_move' || m.code === 'thinking' || m.code === 'not_your_turn') {
      sfx.play('illegal');
    }
    toast(m.message || '操作失败', true);
  }

  _onGameOver(m) {
    this.gameOver = true;
    const isPuzzle = this.mode === 'puzzle';
    let title, cls;
    const humanWin = m.result === (this.humanSide === 'black' ? 'black_win' : 'red_win');
    if (this.mode === 'local_2p') {
      title = m.result === 'red_win' ? '红方胜' : m.result === 'black_win' ? '黑方胜' : '和棋';
      cls = m.result === 'draw' ? 'draw' : 'win';
    } else if (isPuzzle) {
      title = m.result === 'red_win' ? '通关成功' : '挑战失败';
      cls = m.result === 'red_win' ? 'win' : 'lose';
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
      if (m.result === 'red_win') store.setStars(this.puzzle?.id, m.stars);
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
    const goal = this.puzzle.goal === 'win' ? '红先胜' : '红先和';
    const failed = this.puzzle.failed ? '<b style="color:#e05a3a">（已偏离正解，可悔棋或重开）</b>' : '';
    el.hidden = false;
    el.innerHTML = `目标：<b>${goal}</b> · 最少 <b>${this.puzzle.parMoves}</b> 步 · 已走 <b>${this.puzzle.step}</b> 步${failed}
      <br>不用提示且步数达标 = ★★★`;
  }
}
