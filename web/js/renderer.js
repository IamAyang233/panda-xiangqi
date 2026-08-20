// 分层渲染器（A17~A19）：
//   L0 棋盘层（离屏缓存：程序化木纹/刻线/九宫/楚河汉界）
//   L1 棋子层（立体圆片、选中/可落点/最后一手/提示）
//   L2 特效层（走子抛物线动画、吃子粒子、将军震颤、胜利彩带、飘字）
import { parseFEN, parseSq, sqName } from './board.js';
import { pieceSkins, boardSkins, pieceChars } from './themes.js';
import { Particles } from './particles.js';

const easeOutCubic = (t) => 1 - (1 - t) ** 3;

export class BoardRenderer {
  constructor(canvas, opts = {}) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.skin = opts.skin || 'wood';
    this.boardSkin = opts.boardSkin || 'maple';
    this.flipped = !!opts.flipped;
    this.onSquareClick = opts.onSquareClick || (() => {});
    this.interactive = opts.interactive !== false;

    this.board = new Map();      // 'f,r' -> {color,type}
    this.turn = 'red';
    this.selected = null;        // {f,r}
    this.legalTargets = [];
    this.lastMove = null;        // {from:{f,r}, to:{f,r}}
    this.hint = null;            // {from,to}
    this.checkSide = null;
    this.anim = null;            // 走子 tween
    this.floats = [];            // 飘字
    this.particles = new Particles();
    this.shake = 0;
    this.dirty = true;
    this.redrawBoard = true;

    // 画质自适应（A19）
    this.quality = 2;            // 2=全特效 1=粒子减半 0=粒子关闭
    this.fpsAvg = 60;
    this.lowFpsSince = 0;

    this.dpr = Math.min(window.devicePixelRatio || 1, 2);
    this.resize();
    this._loop = this._loop.bind(this);
    this._raf = requestAnimationFrame(this._loop);
    this.lastT = performance.now();

    canvas.addEventListener('pointerdown', (e) => this._onClick(e));
    this._ro = new ResizeObserver(() => { this.resize(); });
    this._ro.observe(canvas.parentElement);
  }

  destroy() {
    cancelAnimationFrame(this._raf);
    this._ro.disconnect();
  }

  setTheme({ skin, boardSkin }) {
    if (skin) this.skin = skin;
    if (boardSkin) this.boardSkin = boardSkin;
    this.redrawBoard = true;
    this.dirty = true;
  }

  setFlipped(v) { this.flipped = v; this.redrawBoard = true; this.dirty = true; }

  setFEN(fen) {
    if (this.anim) this.finishAnim(); // 状态全量同步前结算残留动画
    const { board, turn } = parseFEN(fen);
    this.board = board;
    this.turn = turn;
    this.selected = null;
    this.legalTargets = [];
    this.dirty = true;
  }

  // animateMove 启动抛物线走子动画：自动检测被吃子并在终点爆裂。
  animateMove(fromName, toName) {
    if (this.anim) this.finishAnim(); // 上一动画未播完（如页面被节流）直接结算
    const from = parseSq(fromName), to = parseSq(toName);
    if (!from || !to) return;
    const fromKey = `${from.f},${from.r}`;
    const toKey = `${to.f},${to.r}`;
    const piece = this.board.get(fromKey);
    const victim = this.board.get(toKey) || null;
    if (!piece) { this.dirty = true; return; }
    this.board.delete(fromKey);
    this.board.delete(toKey);
    this.anim = { piece, from, to, victim, t: 0, dur: 0.26 };
    this.dirty = true;
  }

  // animateUndo 悔棋反向动画：棋子从 to 移回 from；若该步吃了子，被吃子恢复在 to。
  // capturedChar 为被吃子的 FEN 字符（如 'r'/'n'），从 renderer 主题映射出真实棋子。
  animateUndo(fromName, toName, capturedChar) {
    if (this.anim) this.finishAnim();
    const from = parseSq(fromName), to = parseSq(toName);
    if (!from || !to) return;
    const toKey = `${to.f},${to.r}`;
    const piece = this.board.get(toKey);
    if (!piece) { this.dirty = true; return; }
    this.board.delete(toKey);
    // 恢复被吃子：按 FEN 字符解析颜色/类型
    let restored = null;
    if (capturedChar) {
      const isRed = capturedChar === capturedChar.toUpperCase();
      const ch = capturedChar.toLowerCase();
      const typeMap = { r: 'rook', n: 'knight', c: 'cannon', b: 'bishop', a: 'advisor', k: 'king', p: 'pawn' };
      const type = typeMap[ch] || 'pawn';
      restored = { color: isRed ? 'red' : 'black', type };
      this.board.set(toKey, restored);
    }
    // 反向：piece 当前在 to，动画移到 from；被吃子已恢复在 to（若 capturedChar）
    this.anim = { piece, from: to, to: from, victim: null, undo: true, t: 0, dur: 0.26 };
    this.dirty = true;
  }

  finishAnim() {
    if (!this.anim) return;
    const { to, piece, victim } = this.anim;
    this.board.set(`${to.f},${to.r}`, piece);
    const pt = this._xy(to.f, to.r);
    if (victim) {
      this.particles.spawn(pt.x, pt.y, {
        count: 30, speed: 170,
        colors: ['#8a5a28', '#a9793f', '#5d3c14', '#d8ae70'],
      });
    }
    this.anim = null;
    this.dirty = true;
  }

  setSelected(sq, targets) {
    this.selected = sq;
    this.legalTargets = targets || [];
    this.dirty = true;
  }

  setLastMove(fromName, toName) {
    if (!fromName) { this.lastMove = null; this.dirty = true; return; }
    this.lastMove = { from: parseSq(fromName), to: parseSq(toName) };
    this.dirty = true;
  }

  setHint(fromName, toName) {
    this.hint = fromName ? { from: parseSq(fromName), to: parseSq(toName) } : null;
    if (this.hint) {
      this.hint.until = performance.now() + 5000;
    }
    this.dirty = true;
  }

  setCheck(side) {
    if (side) {
      this.checkSide = side;
      this.shake = 1;
      setTimeout(() => { this.checkSide = null; this.dirty = true; }, 2600);
    } else {
      this.checkSide = null;
    }
    this.dirty = true;
  }

  floatText(text, color = '#d4a941') {
    const w = this.cssW / 2, h = this.cssH / 2;
    this.floats.push({ text, color, x: w, y: h, t: 0, dur: 1.6 });
    this.dirty = true;
  }

  confetti() { this.particles.confetti(this.cssW, this.cssH); this.dirty = true; }

  // ---------------- 几何 ----------------
  resize() {
    const parent = this.canvas.parentElement;
    if (!parent) return;
    const availW = parent.clientWidth;
    const availH = parent.clientHeight;
    if (availW < 50 || availH < 50) return;
    // 纵线号边距随屏宽自适应（手机更窄 → 棋盘更大；26 保证线号不被裁切）
    const margin = Math.max(26, Math.min(36, availW * 0.06));
    const cell = Math.min((availW - margin * 2) / 8, (availH - margin * 2) / 9);
    this.cell = Math.max(24, cell);
    this.mx = (availW - this.cell * 8) / 2;
    this.my = (availH - this.cell * 9) / 2;
    this.cssW = availW;
    this.cssH = availH;
    this.canvas.style.width = availW + 'px';
    this.canvas.style.height = availH + 'px';
    this.canvas.width = Math.round(availW * this.dpr);
    this.canvas.height = Math.round(availH * this.dpr);
    this.redrawBoard = true;
    this.dirty = true;
  }

  // _xy 交叉点像素坐标（含翻转）
  _xy(f, r) {
    const ff = this.flipped ? 8 - f : f;
    const rr = this.flipped ? r : 9 - r;
    return { x: this.mx + ff * this.cell, y: this.my + rr * this.cell };
  }

  _hit(px, py) {
    const ff = Math.round((px - this.mx) / this.cell);
    const rr = Math.round((py - this.my) / this.cell);
    if (ff < 0 || ff > 8 || rr < 0 || rr > 9) return null;
    const f = this.flipped ? 8 - ff : ff;
    const r = this.flipped ? rr : 9 - rr;
    const pt = this._xy(f, r);
    const dist = Math.hypot(px - pt.x, py - pt.y);
    if (dist > this.cell * 0.52) return null;
    return { f, r };
  }

  _onClick(e) {
    if (!this.interactive) return;
    const rect = this.canvas.getBoundingClientRect();
    const sq = this._hit(e.clientX - rect.left, e.clientY - rect.top);
    if (sq) this.onSquareClick(sq.f, sq.r, sqName(sq.f, sq.r));
  }

  // ---------------- 主循环（A17：脏标记 + 降频 + 画质自适应）----------------
  _loop(t) {
    try {
      const dt = Math.min(0.05, (t - this.lastT) / 1000);
      this.lastT = t;
      this.frameCount = (this.frameCount || 0) + 1;

      // FPS 监控与自动降级
      const fps = 1 / Math.max(dt, 1e-4);
      this.fpsAvg = this.fpsAvg * 0.95 + fps * 0.05;
      if (this.fpsAvg < 45) {
        if (!this.lowFpsSince) this.lowFpsSince = t;
        if (t - this.lowFpsSince > 3000 && this.quality > 0) {
          this.quality--;
          this.particles.density = this.quality;
          this.lowFpsSince = 0;
        }
      } else {
        this.lowFpsSince = 0;
      }

      let animating = false;

      // 走子 tween
      if (this.anim) {
        this.anim.t += dt / this.anim.dur;
        if (this.anim.t >= 1) this.finishAnim();
        else animating = true;
      }
      if (this.shake > 0) {
        this.shake = Math.max(0, this.shake - dt * 1.6);
        animating = true;
      }
      for (const fl of this.floats) {
        fl.t += dt / fl.dur;
        animating = true;
      }
      this.floats = this.floats.filter((f) => f.t < 1);
      if (this.particles.count > 0) {
        this.particles.update(dt);
        animating = true;
      }
      if (this.hint && performance.now() > this.hint.until) {
        this.hint = null;
        this.dirty = true;
      }
      if (this.anim) animating = true;

      if (this.redrawBoard) {
        this._renderBoardLayer();
        this.redrawBoard = false;
        this.dirty = true;
      }
      if (this.dirty || animating) {
        this._renderFrame(t);
        this.dirty = false;
      }
    } catch (err) {
      console.error('render loop:', err); // 循环异常不中断 rAF 链
    }
    requestAnimationFrame(this._loop);
  }

  _renderFrame(t) {
    const ctx = this.ctx;
    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    ctx.clearRect(0, 0, this.cssW, this.cssH);
    // 震颤：整盘偏移
    let ox = 0, oy = 0;
    if (this.shake > 0) {
      const s = this.shake * 5;
      ox = Math.sin(t / 26) * s;
      oy = Math.cos(t / 19) * s * 0.6;
    }
    ctx.save();
    ctx.translate(ox, oy);
    if (this._boardCache) ctx.drawImage(this._boardCache, 0, 0, this.cssW, this.cssH);

    this._drawLastMoveMarkers(ctx);
    this._drawHint(ctx, t);
    this._drawPieces(ctx, t);
    this._drawSelection(ctx, t);
    ctx.restore();

    // 特效层（不随震颤）
    this.particles.draw(ctx);
    for (const fl of this.floats) {
      const y = fl.y - easeOutCubic(fl.t) * 60;
      ctx.save();
      ctx.globalAlpha = 1 - fl.t;
      ctx.font = `700 ${Math.round(this.cell * 0.9)}px "STKaiti","KaiTi",serif`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.shadowColor = 'rgba(0,0,0,0.7)';
      ctx.shadowBlur = 10;
      ctx.fillStyle = fl.color;
      ctx.fillText(fl.text, fl.x, y);
      ctx.restore();
    }
  }

  // ---------------- L0 棋盘层（离屏缓存）----------------
  _renderBoardLayer() {
    const c = document.createElement('canvas');
    c.width = Math.round(this.cssW * this.dpr);
    c.height = Math.round(this.cssH * this.dpr);
    const ctx = c.getContext('2d');
    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    const sk = boardSkins[this.boardSkin] || boardSkins.maple;
    const cell = this.cell, mx = this.mx, my = this.my;

    // 木质底板 + 外框
    const bw = cell * 8 + 56, bh = cell * 9 + 56;
    const bx = mx - 28, by = my - 28;
    ctx.save();
    ctx.shadowColor = 'rgba(0,0,0,0.5)';
    ctx.shadowBlur = 18;
    ctx.fillStyle = sk.border;
    roundRect(ctx, bx, by, bw, bh, 10);
    ctx.fill();
    ctx.restore();

    const grad = ctx.createLinearGradient(bx, by, bx + bw, by + bh);
    grad.addColorStop(0, sk.base1);
    grad.addColorStop(0.5, sk.base2);
    grad.addColorStop(1, sk.base1);
    ctx.fillStyle = grad;
    roundRect(ctx, bx + 7, by + 7, bw - 14, bh - 14, 7);
    ctx.fill();

    // 程序化木纹：水平细纹 + 少量旋纹
    ctx.save();
    roundRect(ctx, bx + 7, by + 7, bw - 14, bh - 14, 7);
    ctx.clip();
    ctx.globalAlpha = 0.08;
    for (let i = 0; i < 90; i++) {
      const y = by + Math.random() * bh;
      ctx.strokeStyle = Math.random() < 0.5 ? '#3c2508' : '#fff6df';
      ctx.lineWidth = 0.6 + Math.random() * 1.4;
      ctx.beginPath();
      ctx.moveTo(bx, y);
      for (let x = bx; x <= bx + bw; x += 24) {
        ctx.lineTo(x, y + Math.sin(x * 0.02 + i) * 2.2);
      }
      ctx.stroke();
    }
    ctx.globalAlpha = 0.05;
    for (let i = 0; i < 7; i++) {
      const x = bx + Math.random() * bw, y = by + Math.random() * bh;
      for (let k = 1; k < 7; k++) {
        ctx.beginPath();
        ctx.ellipse(x, y, k * 4, k * 2.1, 0.4, 0, Math.PI * 2);
        ctx.strokeStyle = '#3c2508';
        ctx.stroke();
      }
    }
    ctx.restore();

    // 刻线
    ctx.strokeStyle = sk.line;
    ctx.lineWidth = 1.4;
    const X = (f) => this._xy(f, 0).x;
    const Y = (r) => this._xy(0, r).y;
    // 横线 10 条
    for (let r = 0; r < 10; r++) {
      line(ctx, X(0), Y(r), X(8), Y(r));
    }
    // 纵线 9 条（中间断河）
    for (let f = 0; f <= 8; f++) {
      if (f === 0 || f === 8) {
        line(ctx, X(f), Y(0), X(f), Y(9));
      } else {
        line(ctx, X(f), Y(0), X(f), Y(4));
        line(ctx, X(f), Y(5), X(f), Y(9));
      }
    }
    // 九宫斜线
    ctx.lineWidth = 1.1;
    line(ctx, X(3), Y(0), X(5), Y(2));
    line(ctx, X(5), Y(0), X(3), Y(2));
    line(ctx, X(3), Y(7), X(5), Y(9));
    line(ctx, X(5), Y(7), X(3), Y(9));

    // 炮位 / 兵位标记（十字角）
    const marks = [[1, 2], [7, 2], [0, 3], [2, 3], [4, 3], [6, 3], [8, 3],
                   [1, 7], [7, 7], [0, 6], [2, 6], [4, 6], [6, 6], [8, 6]];
    for (const [f, r] of marks) this._posMark(ctx, X(f), Y(r), sk.line, f === 0, f === 8);

    // 楚河 漢界（居中于 X(2)/X(6)；用 textAlign=center 避免右侧文字溢出棋盘外框）
    ctx.fillStyle = sk.text;
    ctx.textBaseline = 'middle';
    ctx.textAlign = 'center';
    const fs = Math.round(cell * 0.62);
    ctx.font = `600 ${fs}px "STKaiti","KaiTi","Noto Serif SC",serif`;
    const riverY = (Y(4) + Y(5)) / 2;
    ctx.globalAlpha = 0.8;
    ctx.fillText('楚  河', X(2), riverY);
    ctx.save();
    ctx.translate(X(6), riverY);
    ctx.rotate(Math.PI);
    ctx.fillText('漢  界', 0, 0);
    ctx.restore();
    ctx.globalAlpha = 1;

    // 边缘纵线号
    ctx.font = `500 ${Math.round(cell * 0.3)}px "STKaiti","KaiTi",serif`;
    ctx.fillStyle = sk.coord;
    const numsCn = ['九', '八', '七', '六', '五', '四', '三', '二', '一'];
    const numsAr = ['1', '2', '3', '4', '5', '6', '7', '8', '9'];
    for (let f = 0; f <= 8; f++) {
      const isRedView = !this.flipped;
      const label = isRedView ? numsCn[f] : numsAr[f];
      ctx.textAlign = 'center';
      ctx.fillText(label, X(f), Y(9) + cell * 0.55);
      const label2 = isRedView ? numsAr[f] : numsCn[f];
      ctx.fillText(label2, X(f), Y(0) - cell * 0.55);
    }

    this._boardCache = c;
  }

  _posMark(ctx, x, y, color, noLeft, noRight) {
    const d = this.cell * 0.11, g = this.cell * 0.07;
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.2;
    for (const [sx, sy] of [[-1, -1], [1, -1], [-1, 1], [1, 1]]) {
      if ((noLeft && sx < 0) || (noRight && sx > 0)) continue;
      ctx.beginPath();
      ctx.moveTo(x + sx * g, y + sy * (g + d));
      ctx.lineTo(x + sx * g, y + sy * g);
      ctx.lineTo(x + sx * (g + d), y + sy * g);
      ctx.stroke();
    }
  }

  // ---------------- L1 棋子层 ----------------
  _drawPieces(ctx, t) {
    const animKey = this.anim ? `${this.anim.to.f},${this.anim.to.r}` : null;
    const animFromKey = this.anim ? `${this.anim.from.f},${this.anim.from.r}` : null;

    // 被吃子：在动画前半段淡出
    if (this.anim && this.anim.victim && this.anim.t < 0.4) {
      const p = this._xy(this.anim.to.f, this.anim.to.r);
      this._drawPiece(ctx, p.x, p.y, this.anim.victim, { alpha: 1 - this.anim.t / 0.4 });
    }

    for (const [key, piece] of this.board) {
      if (key === animKey) continue;
      const [f, r] = key.split(',').map(Number);
      const isShaking = this.checkSide && piece.type === 1 &&
        piece.color === this.checkSide && !this.anim;
      const p = this._xy(f, r);
      let dx = 0;
      if (isShaking) dx = Math.sin(t / 22) * 3.5;
      this._drawPiece(ctx, p.x + dx, p.y, piece, {});
    }

    // 动画中的棋子（A17：easeOutCubic + 抛物线抬升 + 缩放）
    if (this.anim) {
      const a = this.anim;
      const p0 = this._xy(a.from.f, a.from.r);
      const p1 = this._xy(a.to.f, a.to.r);
      const e = easeOutCubic(a.t);
      const x = p0.x + (p1.x - p0.x) * e;
      const y = p0.y + (p1.y - p0.y) * e - Math.sin(Math.PI * a.t) * this.cell * 0.5;
      const scale = 1 + 0.12 * Math.sin(Math.PI * a.t);
      this._drawPiece(ctx, x, y, a.piece, { scale });
    }
  }

  _drawPiece(ctx, x, y, piece, { alpha = 1, scale = 1 } = {}) {
    const R = this.cell * 0.44 * scale;
    const skin = pieceSkins[this.skin] || pieceSkins.wood;
    const colors = piece.color === 'red' ? skin.red : skin.black;
    const char = pieceChars[piece.color][piece.type - 1];

    ctx.save();
    ctx.globalAlpha = alpha;

    // 投影
    ctx.fillStyle = skin.shadow;
    ctx.beginPath();
    ctx.ellipse(x + R * 0.08, y + R * 0.22, R * 0.98, R * 0.88, 0, 0, Math.PI * 2);
    ctx.filter = this.quality >= 2 ? 'blur(2px)' : 'none';
    ctx.fill();
    ctx.filter = 'none';

    // 盘体：径向渐变模拟立体
    const g = ctx.createRadialGradient(x - R * 0.35, y - R * 0.4, R * 0.1, x, y, R);
    g.addColorStop(0, colors.face1);
    g.addColorStop(0.75, colors.face2);
    g.addColorStop(1, colors.rim);
    ctx.beginPath();
    ctx.arc(x, y, R, 0, Math.PI * 2);
    ctx.fillStyle = g;
    ctx.fill();

    // 外缘
    ctx.lineWidth = Math.max(1.5, R * 0.09);
    ctx.strokeStyle = colors.rim;
    ctx.stroke();

    // 内环
    ctx.beginPath();
    ctx.arc(x, y, R * 0.78, 0, Math.PI * 2);
    ctx.lineWidth = Math.max(1, R * 0.055);
    ctx.strokeStyle = colors.ring;
    ctx.globalAlpha = alpha * 0.75;
    ctx.stroke();
    ctx.globalAlpha = alpha;

    // 字
    ctx.font = `700 ${Math.round(R * 1.02)}px "STKaiti","KaiTi","SimKai","Noto Serif SC",serif`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillStyle = colors.text;
    ctx.shadowColor = 'rgba(255,255,255,0.35)';
    ctx.shadowOffsetY = -0.8;
    ctx.shadowBlur = this.quality >= 2 ? 1.5 : 0;
    ctx.fillText(char, x, y + R * 0.05);
    ctx.restore();
  }

  _drawSelection(ctx, t) {
    // 选中棋子光环
    if (this.selected) {
      const p = this._xy(this.selected.f, this.selected.r);
      const R = this.cell * 0.48;
      ctx.save();
      ctx.strokeStyle = '#ffd873';
      ctx.lineWidth = 3;
      ctx.shadowColor = '#ffd873';
      ctx.shadowBlur = this.quality >= 1 ? 14 : 0;
      ctx.beginPath();
      ctx.arc(p.x, p.y, R + 2 + Math.sin(t / 200) * 1.4, 0, Math.PI * 2);
      ctx.stroke();
      ctx.restore();
    }
    // 可落点
    for (const name of this.legalTargets) {
      const sq = parseSq(name);
      if (!sq) continue;
      const p = this._xy(sq.f, sq.r);
      const occupied = this.board.has(`${sq.f},${sq.r}`);
      ctx.save();
      if (occupied) {
        ctx.strokeStyle = 'rgba(224,90,58,0.95)';
        ctx.lineWidth = 3;
        ctx.setLineDash([6, 5]);
        ctx.beginPath();
        ctx.arc(p.x, p.y, this.cell * 0.5, 0, Math.PI * 2);
        ctx.stroke();
      } else {
        ctx.fillStyle = 'rgba(212,169,65,0.85)';
        ctx.shadowColor = '#d4a941';
        ctx.shadowBlur = this.quality >= 1 ? 8 : 0;
        ctx.beginPath();
        ctx.arc(p.x, p.y, this.cell * 0.1, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.restore();
    }
  }

  _drawLastMoveMarkers(ctx) {
    if (!this.lastMove) return;
    for (const sq of [this.lastMove.from, this.lastMove.to]) {
      if (!sq) continue;
      const p = this._xy(sq.f, sq.r);
      const d = this.cell * 0.4;
      ctx.save();
      ctx.strokeStyle = 'rgba(212,169,65,0.9)';
      ctx.lineWidth = 2.4;
      for (const [sx, sy] of [[-1, -1], [1, -1], [-1, 1], [1, 1]]) {
        ctx.beginPath();
        ctx.moveTo(p.x + sx * d, p.y + sy * (d - d * 0.45));
        ctx.lineTo(p.x + sx * d, p.y + sy * d);
        ctx.lineTo(p.x + sx * (d - d * 0.45), p.y + sy * d);
        ctx.stroke();
      }
      ctx.restore();
    }
  }

  _drawHint(ctx, t) {
    if (!this.hint) return;
    const pulse = 0.6 + 0.4 * Math.sin(t / 160);
    for (const sq of [this.hint.from, this.hint.to]) {
      if (!sq) continue;
      const p = this._xy(sq.f, sq.r);
      ctx.save();
      ctx.strokeStyle = `rgba(126,201,126,${pulse})`;
      ctx.lineWidth = 3;
      ctx.beginPath();
      ctx.arc(p.x, p.y, this.cell * 0.5, 0, Math.PI * 2);
      ctx.stroke();
      ctx.restore();
    }
  }

  _ring(x, y) {
    // 冲击波光环（简易：由飘字系统承担视觉即可，此处保留接口）
  }
}

function line(ctx, x1, y1, x2, y2) {
  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();
}

function roundRect(ctx, x, y, w, h, r) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}
