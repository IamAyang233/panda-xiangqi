// 摆局屏：自己摆一个局面，存成自定义残局（服务端落盘、全服共享），可直接进入挑战。
//
// 与残局列表的区别：那些是预置题库，这里是玩家现摆现玩。保存后再挑战，
// 因此走的是与内置残局完全相同的 ModePuzzle 链路（不需要「临时 FEN 开局」那条路）。
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { toast, showScreen, confirmDialog } from '../ui.js';
import { BoardRenderer } from '../renderer.js';
import { boardToFEN } from '../board.js';
import { pieceChars } from '../themes.js';
import { savePuzzle } from '../net.js';

const $ = (id) => document.getElementById(id);

// 与大厅同一套档位名（16 档）。摆局时选的档位会被服务端透传给守方引擎 ——
// 自摆残局的难度不是内置五档，puzzleLevel 会改用这个值。
const levelNames = [
  [1, '入门'], [2, '初学'], [3, '业余初级'], [4, '业余三级'],
  [5, '业余五级'], [6, '业余七级'], [7, '业余九级'], [8, '县市级'],
  [9, '市冠军'], [10, '省冠军'], [11, '省强手'], [12, '国家大师'],
  [13, '国家强手'], [14, '特大水准'], [15, '特大强棋'], [16, '特大全力'],
];

const EMPTY_FEN = '9/9/9/9/9/9/9/9/9/9 w';

let renderer = null;
let onStart = null;      // 由 main.js 注入：onStart(mode, opts) 进入对局
let brush = null;        // {color,type} 或 null（橡皮）
let history = [];        // 撤销栈：{f,r,prev}，prev=null 表示该格原为空
let side = 'red';
let goal = 'win';
let level = 4;
let saving = false;

// showSetup 进入摆局屏，并显式重算一次棋盘几何。
//
// 为什么必须显式调用：本模块的 renderer 是在页面初始化时构造的，那一刻屏幕还是
// hidden（display:none），父容器尺寸为 0，几何被算成 0；屏幕变可见后若只依赖
// ResizeObserver 的防抖回调，移动端实测会出现「棋盘整块空白、始终不画」。
// 对局屏早就为同一个坑在 showScreen 之后显式调了一次 renderer.resize()。
export function showSetup() {
  showScreen('setup');
  requestAnimationFrame(() => renderer?.resize());
}

export function initSetup(handler) {
  onStart = handler;
  renderer = new BoardRenderer($('setup-canvas'), {
    skin: store.theme.pieces,
    boardSkin: store.theme.board,
    onSquareClick: (f, r) => onSquare(f, r),
  });
  renderer.particles.enabled = store.theme.particles;
  renderer.setFEN(EMPTY_FEN); // 空盘起摆（按拍板口径不做 FEN 导入）

  renderPalette();
  renderLevels();

  $('btn-eraser').onclick = () => { sfx.play('button'); setBrush(null); };
  $('btn-undo-piece').onclick = () => { sfx.play('button'); undoOne(); };
  $('btn-clear-board').onclick = async () => {
    sfx.play('button');
    if (!renderer.board.size) { toast('棋盘本来就是空的', false, 1600); return; }
    if (await confirmDialog('清空棋盘上的所有棋子？', { danger: true, okText: '清空' })) {
      resetBoard();
      refreshStatus();
    }
  };
  document.querySelectorAll('#screen-setup .side-btn').forEach((b) => {
    b.onclick = () => {
      sfx.play('button');
      document.querySelectorAll('#screen-setup .side-btn').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      side = b.dataset.side;
      refreshStatus(); // 轮走方变了，照面/无着法判定的前提也跟着变
    };
  });
  document.querySelectorAll('#screen-setup .goal-btn').forEach((b) => {
    b.onclick = () => {
      sfx.play('button');
      document.querySelectorAll('#screen-setup .goal-btn').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      goal = b.dataset.goal;
      refreshStatus();
    };
  });

  $('btn-save-play').onclick = () => save(true);
  $('btn-save-only').onclick = () => save(false);

  // 竖排布局（手机/平板竖屏）默认折叠「对局设置」：那是一次性设置，长期占着会把棋盘
  // 压小；调色板、校验条与两个按钮保持常驻，随时可摆子/保存。宽屏下这条开关不显示，
  // collapsed 类也只在该断点内生效（见 main.css）。
  const panel = $('setup-panel');
  const toggle = $('setup-toggle');
  const setCollapsed = (on) => {
    panel.classList.toggle('collapsed', on);
    toggle.setAttribute('aria-expanded', String(!on));
  };
  setCollapsed(window.matchMedia('(max-width: 860px)').matches);
  toggle.onclick = () => {
    sfx.play('button');
    setCollapsed(!panel.classList.contains('collapsed'));
  };

  refreshStatus();

  // 供自动化断言使用（与对局屏 window.__qijing 同款约定）
  window.__qijingSetup = {
    renderer,
    brush: () => brush,
    place: (f, r) => onSquare(f, r),
    fen: () => boardToFEN(renderer.board, side),
    status: () => validate(),
    reset: () => { resetBoard(); refreshStatus(); },
  };
}

// 画笔栏：红黑各 7 兵种（type 1..7 与 renderer 的 pieceChars[color][type-1] 对应）
function renderPalette() {
  for (const color of ['red', 'black']) {
    const box = $(`palette-${color}`);
    box.innerHTML = '';
    pieceChars[color].forEach((ch, i) => {
      const btn = document.createElement('button');
      btn.className = 'palette-btn';
      btn.dataset.color = color;
      btn.dataset.type = String(i + 1);
      btn.innerHTML = `<span class="palette-piece ${color}">${ch}</span>`;
      btn.onclick = () => { sfx.play('button'); setBrush({ color, type: i + 1 }); };
      box.appendChild(btn);
    });
  }
}

function renderLevels() {
  const el = $('setup-level');
  const now = $('setup-level-now');
  const nameOf = (v) => (levelNames.find(([n]) => n === v) || [0, ''])[1];
  const sync = () => {
    level = +el.value;
    const name = nameOf(level);
    now.textContent = `${level} · ${name}`;
    // 让读屏软件念出档位名，而不是只念一个数字
    el.setAttribute('aria-valuetext', `第 ${level} 档 ${name}`);
  };
  el.value = String(level);
  sync();
  el.oninput = () => {
    const prev = now.textContent;
    sync();
    // 拖动时 input 事件很密集，只在**档位真的变了**时给一声，避免连响成噪声
    if (prev !== now.textContent) sfx.play('button');
  };
}

function setBrush(b) {
  brush = b;
  document.querySelectorAll('.palette-btn').forEach((el) => el.classList.remove('active'));
  $('btn-eraser').classList.remove('active-pick');
  if (b) {
    document.querySelector(`.palette-btn[data-color="${b.color}"][data-type="${b.type}"]`)?.classList.add('active');
    $('setup-hint').textContent = `已选 ${pieceChars[b.color][b.type - 1]}，点棋盘落子；点已有棋子可拿走它`;
  } else {
    $('btn-eraser').classList.add('active-pick');
    $('setup-hint').textContent = '橡皮：点棋盘上的棋子把它拿走';
  }
}

// 点击棋盘：有棋子则删除（任何画笔下都允许，比先切橡皮少一步），空格则放当前画笔。
function onSquare(f, r) {
  const key = `${f},${r}`;
  const prev = renderer.board.get(key) || null;
  if (prev) {
    history.push({ f, r, prev });
    renderer.board.delete(key);
  } else if (brush) {
    history.push({ f, r, prev: null });
    renderer.board.set(key, { color: brush.color, type: brush.type });
  } else {
    toast('先在上面选一枚棋子', false, 1600);
    return;
  }
  renderer.dirty = true;
  refreshStatus();
}

function undoOne() {
  const last = history.pop();
  if (!last) { toast('没有可撤销的操作', false, 1600); return; }
  const key = `${last.f},${last.r}`;
  if (last.prev) renderer.board.set(key, last.prev);
  else renderer.board.delete(key);
  renderer.dirty = true;
  refreshStatus();
}

function findPiece(board, color, type) {
  for (const [k, p] of board) {
    if (p.color === color && p.type === type) {
      const [f, r] = k.split(',').map(Number);
      return { f, r };
    }
  }
  return null;
}

function countPieces(board, color, type) {
  let n = 0;
  for (const p of board.values()) if (p.color === color && p.type === type) n++;
  return n;
}

function inPalace(p, color) {
  if (!p) return false;
  const okRank = color === 'red' ? p.r <= 2 : p.r >= 7;
  return okRank && p.f >= 3 && p.f <= 5;
}

// 客户端预检：与服务的四条硬校验同口径，但**只是提示**（服务端才是唯一防线）。
// 逐条说清缺什么，而不是只给一句「局面不合法」——摆局最容易缺的就是王。
function validate() {
  const fen = boardToFEN(renderer.board, side);
  const errs = [];
  const rk = countPieces(renderer.board, 'red', 1);
  const bk = countPieces(renderer.board, 'black', 1);
  if (rk !== 1) errs.push(rk === 0 ? '缺少红方帅' : `红方有 ${rk} 个帅（只能一个）`);
  if (bk !== 1) errs.push(bk === 0 ? '缺少黑方将' : `黑方有 ${bk} 个将（只能一个）`);
  if (rk === 1 && !inPalace(findPiece(renderer.board, 'red', 1), 'red')) errs.push('红帅必须在九宫内（下方 3×3 交叉点）');
  if (bk === 1 && !inPalace(findPiece(renderer.board, 'black', 1), 'black')) errs.push('黑将必须在九宫内（上方 3×3 交叉点）');
  if (rk === 1 && bk === 1) {
    const r = findPiece(renderer.board, 'red', 1);
    const b = findPiece(renderer.board, 'black', 1);
    if (r && b && r.f === b.f) {
      let blocked = false;
      for (let rr = Math.min(r.r, b.r) + 1; rr < Math.max(r.r, b.r); rr++) {
        if (renderer.board.has(`${r.f},${rr}`)) { blocked = true; break; }
      }
      if (!blocked) errs.push('将帅照面（同一列中间无子）');
    }
  }
  return { fen, errs };
}

function refreshStatus() {
  const { errs } = validate();
  const el = $('setup-status');
  if (errs.length) {
    el.className = 'setup-status bad';
    el.textContent = `还差：${errs.join('；')}`;
  } else {
    el.className = 'setup-status good';
    el.textContent = `局面就绪（${renderer.board.size} 枚棋子）· 可保存并挑战`;
  }
  const ok = errs.length === 0;
  $('btn-save-play').disabled = !ok;
  $('btn-save-only').disabled = !ok;
}

function defaultName() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `自摆残局 ${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function resetBoard() {
  renderer.setFEN(EMPTY_FEN);
  history = [];
  const nameEl = $('setup-name');
  if (nameEl) nameEl.value = '';
}

async function save(andPlay) {
  if (saving) return;
  const { fen, errs } = validate();
  if (errs.length) { toast(`还差：${errs[0]}`, true, 2600); return; }
  saving = true;
  $('btn-save-play').disabled = true;
  $('btn-save-only').disabled = true;
  try {
    const p = await savePuzzle({ name: ($('setup-name').value || '').trim() || defaultName(), fen, side, goal });
    sfx.play('star');
    if (andPlay) {
      toast('已保存，开始挑战', false, 1800);
      resetBoard();
      // 按 id 走与内置残局完全相同的入口，因此残局链路一行都不用改。
      onStart?.('puzzle', { puzzleId: p.id, level, from: 'setup' });
    } else {
      toast('已保存到自定义残局', false, 1800);
      resetBoard();
      showScreen('lobby');
    }
  } catch (e) {
    // 服务端的校验原因直接展示：它是唯一防线，可能拦下客户端预检没覆盖的情况。
    toast(`保存失败：${e.message}`, true, 3600);
    sfx.play('illegal');
  } finally {
    saving = false;
    refreshStatus();
  }
}
