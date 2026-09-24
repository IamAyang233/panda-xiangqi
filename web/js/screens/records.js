// 棋谱复盘：列表（看每一局）→ 详情（回放 + 关键手 + 问 AI 讲解）。
//
// 回放**不复用 GameScreen**：那个类与「创建对局 + WebSocket 连接」强绑定
// （start() 会去 createGame），而复盘只是本地读一份记录逐手摆出来。这里照
// 摆局屏（setup.js）的先例：自己一块 canvas + 一个独立 BoardRenderer，
// 进入时按三拍重算几何（屏幕刚从 display:none 切回来时父容器还没有尺寸）。
//
// 回放的逐手推进也不问服务端：记录里已有起始 FEN 与每一手的 from/to，
// 在本地 board Map 上搬子即可（象棋没有升变，搬子无需任何规则判断）。
import { BoardRenderer } from '../renderer.js';
import { parseFEN, boardToFEN, parseSq, sqName } from '../board.js';
import { listRecords, getRecord, deleteRecord, analyzeRecord, reviewRecord } from '../net.js';
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { toast, confirmDialog, showScreen } from '../ui.js';

const $ = (id) => document.getElementById(id);

let renderer = null;
let all = [];            // 列表（累计，翻页往这里加）
let total = 0;
let page = 0;
let cur = null;          // 当前打开的棋谱（服务端返回的完整记录）
let step = 0;            // 回放进度：0 = 起始局面，k = 走完前 k 手
let frames = [];         // 每一手的 {from,to,captured,uci,cn} 解析结果（懒构建）
let baseBoard = null;    // 起始局面的 board
let baseTurn = 'red';
let keys = [];           // 关键手
let keysLoading = false;
let reviewing = false;

const esc = (s) => String(s == null ? '' : s)
  .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

const MODE_TEXT = { engine: '人机', llm: '大模型', local_2p: '双人' };
const RESULT_TEXT = { red_win: '红方胜', black_win: '黑方胜', draw: '和棋' };
const REASON_TEXT = {
  checkmate: '将死', stalemate: '困毙', resign: '认输', repetition: '三次重复',
  long_check: '长将判负', '60_moves': '60 回合', insufficient: '子力不足',
};
const TAG_TEXT = { capture: '吃子', check: '将军', mate: '终局', blunder: '失误', mistake: '疑问手' };

export async function initRecords() {
  renderer = new BoardRenderer($('record-board'), { interactive: false });
  $('btn-record-more').onclick = () => { sfx.play('button'); loadPage(page + 1); };
  $('btn-rec-back').onclick = () => { sfx.play('button'); showList(); };
  $('btn-rec-first').onclick = () => { sfx.play('button'); gotoStep(0); };
  $('btn-rec-prev').onclick = () => { sfx.play('button'); gotoStep(step - 1); };
  $('btn-rec-next').onclick = () => { sfx.play('button'); gotoStep(step + 1); };
  $('btn-rec-last').onclick = () => { sfx.play('button'); gotoStep(frames.length); };
  $('btn-rec-flip').onclick = () => { sfx.play('button'); renderer.setFlipped(!renderer.flipped); };
  $('btn-rec-review').onclick = () => { askAI(); };
  // 视口变化（手机横竖屏切换、浏览器窗口改大小）后画布要重算尺寸：
  // 画布尺寸由 js 按父容器实测值写死，纯 CSS 变不了它。
  let rt = null;
  window.addEventListener('resize', () => {
    clearTimeout(rt);
    rt = setTimeout(() => { try { renderer.resize(); } catch { /* 屏未显示时忽略 */ } }, 120);
  });
  $('record-moves').onclick = (e) => {
    const row = e.target.closest('[data-step]');
    if (!row) return;
    sfx.play('button');
    gotoStep(+row.dataset.step);
  };
}

// showRecords 进入棋谱屏：每次都重新拉列表（棋谱存在服务端、全服共享，
// 别的设备刚存的这一局要能立刻看到 —— 与残局列表同一个理由）。
export async function showRecords() {
  showScreen('records');
  $('record-list').hidden = false;
  $('record-detail').hidden = true;
  await loadPage(0);
}

async function loadPage(p) {
  const grid = $('record-grid');
  if (p === 0) {
    all = [];
    grid.innerHTML = '<div class="list-loading"><span class="ring"></span>正在载入棋谱…</div>';
  }
  let data;
  try {
    data = await listRecords(p);
  } catch (e) {
    grid.innerHTML = '';
    toast('棋谱加载失败：' + e.message, true, 3200);
    $('record-count').textContent = '';
    return;
  }
  if (p === 0) grid.innerHTML = '';
  page = p;
  total = data.total || 0;
  const items = data.items || [];
  all = all.concat(items);
  for (const r of items) grid.appendChild(card(r));
  $('record-count').textContent = total ? `共 ${total} 局` : '';
  $('btn-record-more').hidden = all.length >= total;
  if (!all.length) {
    grid.innerHTML = '<div class="record-empty">还没有棋谱。下完一局后，在结算弹窗点「保存此局」即可存进来。</div>';
  }
}

function card(r) {
  const el = document.createElement('button');
  el.className = 'puzzle-card record-card';
  const mode = MODE_TEXT[r.mode] || r.mode;
  const sub = r.mode === 'engine' && r.level ? ` · 第 ${r.level} 档`
    : r.mode === 'llm' && r.model ? ` · ${esc(r.model)}` : '';
  const result = RESULT_TEXT[r.result] || r.result;
  const reason = REASON_TEXT[r.reason] ? `（${REASON_TEXT[r.reason]}）` : '';
  el.innerHTML = `
    <div class="puzzle-name">${esc(mode)}${sub}</div>
    <div class="puzzle-meta">我执${r.humanSide === 'black' ? '黑' : '红'} · ${r.moveCount} 手 · ${esc(r.created || '')}</div>
    <div class="puzzle-goal-tag ${r.result === 'draw' ? 'draw' : 'win'}">${esc(result)}${reason}</div>
    <div class="record-badges">${r.analyzed ? '<span class="puzzle-own">已分析</span>' : ''}${r.reviewedCount ? `<span class="puzzle-own">讲解 ${r.reviewedCount}</span>` : ''}</div>
    <span class="puzzle-del" title="删除这条棋谱" data-del="${esc(r.id)}">
      <svg class="icon"><use href="#i-trash"/></svg>
    </span>`;
  el.onclick = (e) => {
    const del = e.target.closest('[data-del]');
    if (del) { e.stopPropagation(); removeRecord(r, del); return; }
    sfx.play('button');
    openDetail(r.id);
  };
  return el;
}

async function removeRecord(r, el) {
  const ok = await confirmDialog(`删除后无法恢复，确定删除这局棋谱（${r.created || r.id}）吗？`, { danger: true, okText: '删除' });
  if (!ok) return;
  el.style.pointerEvents = 'none';
  try {
    await deleteRecord(r.id);
    all = all.filter((x) => x.id !== r.id);
    total = Math.max(0, total - 1);
    el.closest('.puzzle-card').remove();
    $('record-count').textContent = total ? `共 ${total} 局` : '';
    // 删掉最后一条时补上空态提示：否则列表区只剩一片空白，
    // 看不出是「删干净了」还是「加载失败了」。
    if (!all.length) {
      $('record-grid').innerHTML = '<div class="record-empty">还没有棋谱。下完一局后，在结算弹窗点「保存此局」即可存进来。</div>';
    }
    toast('已删除', false, 1600);
  } catch (e) {
    el.style.pointerEvents = '';
    toast('删除失败：' + e.message, true, 3200);
  }
}

// ---------------------------------------------------------------- 详情

async function openDetail(id) {
  let rec;
  try {
    rec = await getRecord(id);
  } catch (e) {
    toast('打开失败：' + e.message, true, 3200);
    return;
  }
  cur = rec;
  reviewing = false; // 上一条记录的「讲解中」标志不能留给这一条（按钮会一直禁用）
  keys = rec.analysis || [];
  keysLoading = false;
  $('record-list').hidden = true;
  $('record-detail').hidden = false;
  // 屏幕从隐藏切回可见：父容器此时才有尺寸，三拍各算一次（与摆局屏同款处理）
  const kick = () => { try { renderer.resize(); } catch { /* 下一拍还会再试 */ } };
  kick(); requestAnimationFrame(kick); setTimeout(kick, 120);
  buildFrames();
  renderMeta();
  renderMoveList();
  renderKeys();
  renderer.setFlipped(rec.humanSide === 'black'); // 默认按「我当时执哪方」摆
  gotoStep(frames.length); // 打开就停在终局，往回翻
  // 关键手第一次打开才让服务端算（几秒），有缓存立刻回。
  // 判据用服务端的 analyzed 标记而不是「列表非空」：干净对局本来就没有关键手，
  // 拿列表长度当判据会让它每次打开都重算一遍整局。
  if (!rec.analyzed) loadAnalysis();
  setReviewText(rec, step);
}

function buildFrames() {
  const parsed = parseFEN(cur.startFen);
  baseBoard = parsed.board;
  baseTurn = parsed.turn;
  frames = (cur.moves || []).map((m) => {
    const f = parseSq(m.uci.slice(0, 2));
    const t = parseSq(m.uci.slice(2, 4));
    return { from: f, to: t, uci: m.uci, cn: m.cn, captured: m.captured || '' };
  });
}

// boardAt 返回走完前 k 手之后的棋盘（起始局面的副本上搬子）。
function boardAt(k) {
  const b = new Map();
  for (const [key, v] of baseBoard) b.set(key, { ...v });
  for (let i = 0; i < k; i++) {
    const { from, to } = frames[i];
    if (!from || !to) continue;
    const piece = b.get(`${from.f},${from.r}`);
    b.delete(`${from.f},${from.r}`);
    if (piece) b.set(`${to.f},${to.r}`, piece);
  }
  return b;
}

function turnAt(k) {
  return k % 2 === 0 ? baseTurn : (baseTurn === 'red' ? 'black' : 'red');
}

function gotoStep(k) {
  if (!cur) return;
  if (k < 0) k = 0;
  if (k > frames.length) k = frames.length;
  step = k;
  const board = boardAt(step);
  renderer.setFEN(boardToFEN(board, turnAt(step)));
  if (step > 0) {
    const f = frames[step - 1];
    renderer.setLastMove(sqName(f.from.f, f.from.r), sqName(f.to.f, f.to.r));
  } else {
    renderer.setLastMove(null, null);
  }
  $('record-step').textContent = `${step} / ${frames.length}`;
  $('btn-rec-prev').disabled = step === 0;
  $('btn-rec-first').disabled = step === 0;
  $('btn-rec-next').disabled = step === frames.length;
  $('btn-rec-last').disabled = step === frames.length;
  highlightMoveRow();
  if (cur) setReviewText(cur, step);
}

function renderMeta() {
  const r = cur;
  const mode = MODE_TEXT[r.mode] || r.mode;
  const sub = r.mode === 'engine' && r.level ? `第 ${r.level} 档`
    : r.mode === 'llm' && r.model ? esc(r.model) : '';
  $('record-title').textContent = '棋谱';
  $('record-meta').innerHTML =
    `<div>${esc(mode)}${sub ? ' · ' + sub : ''}</div>` +
    `<div>我执${r.humanSide === 'black' ? '黑' : '红'} · ${(r.moves || []).length} 手</div>` +
    `<div>${esc(RESULT_TEXT[r.result] || r.result)}${REASON_TEXT[r.reason] ? '（' + REASON_TEXT[r.reason] + '）' : ''} · ${esc(r.created || '')}</div>`;
}

function renderMoveList() {
  const box = $('record-moves');
  box.innerHTML = '';
  const rows = [];
  for (let i = 0; i < frames.length; i += 2) {
    const no = i / 2 + 1;
    const red = frames[i];
    const black = frames[i + 1];
    const row = document.createElement('div');
    row.className = 'mv-row';
    row.dataset.step0 = String(i + 1);
    row.innerHTML = `<span class="mv-no">${no}.</span>` +
      `<span class="mv-red" data-step="${i + 1}">${esc(red ? red.cn : '')}</span>` +
      `<span class="mv-black" data-step="${i + 2}">${esc(black ? black.cn : '')}</span>`;
    box.appendChild(row);
    rows.push(row);
  }
}

function highlightMoveRow() {
  document.querySelectorAll('#record-moves .mv-row').forEach((row) => {
    const first = +row.dataset.step0;
    const on = step >= first && step <= first + 1;
    row.classList.toggle('mv-cur', on);
  });
  const curRow = document.querySelector('#record-moves .mv-cur');
  if (curRow) curRow.scrollIntoView({ block: 'nearest' });
}

function renderKeys() {
  const box = $('record-keys');
  if (keysLoading) { box.innerHTML = '<div class="record-keys-loading">正在分析关键手…</div>'; return; }
  if (!keys.length) {
    box.innerHTML = cur && cur.analyzed
      ? '<div class="record-keys-title">关键手</div><div class="record-keys-loading">未发现明显的关键手（本局没有吃子/将军，引擎也没看出明显失误）</div>'
      : '';
    return;
  }
  box.innerHTML = '<div class="record-keys-title">关键手</div>' + keys.map((k) => {
    const tag = TAG_TEXT[k.tag] || k.tag;
    const cn = (frames[k.index] && frames[k.index].cn) || `第 ${k.index + 1} 手`;
    const delta = k.delta ? ` −${k.delta}` : '';
    return `<button class="key-chip ${k.tag}" data-key="${k.index}">${esc(cn)} · ${esc(tag)}${delta}</button>`;
  }).join('');
  box.querySelectorAll('[data-key]').forEach((el) => {
    el.onclick = () => { sfx.play('button'); gotoStep(+el.dataset.key + 1); };
  });
}

async function loadAnalysis() {
  keysLoading = true;
  renderKeys();
  const id = cur.id;
  try {
    const out = await analyzeRecord(id);
    if (!cur || cur.id !== id) return; // 切走了就别写回来
    keys = out.analysis || [];
    cur.analyzed = true;
  } catch (e) {
    keys = [];
    toast('关键手分析失败：' + e.message, true, 3200);
  } finally {
    keysLoading = false;
    if (cur && cur.id === id) { renderKeys(); setReviewText(cur, step); }
  }
}

// setReviewText 显示当前这一手的讲解（有缓存就直接给，没有就提示可以问）
function setReviewText(rec, k) {
  const box = $('record-review-text');
  const btn = $('btn-rec-review');
  const label = $('btn-rec-review-label');
  if (k <= 0) {
    box.textContent = '点「下一手」或着法列表里的任一手，就能对着那一手问 AI。';
    btn.disabled = true;
    label.textContent = '问 AI 讲解这一手';
    return;
  }
  const cached = (rec.reviews || {})[String(k - 1)];
  if (cached) {
    box.textContent = cached;
  } else {
    box.textContent = '这一手还没有讲解。点下面的按钮问 AI —— 重推理模型可能要一分钟左右，结果会存进棋谱，下次直接看。';
  }
  btn.disabled = reviewing;
  label.textContent = cached ? '重新问 AI' : '问 AI 讲解这一手';
}

async function askAI() {
  if (reviewing || step <= 0 || !cur) return;
  // 记下发起时的记录 id：讲解要几十秒，期间用户可能返回列表或打开别的记录，
  // 那时把结果写回界面会串到另一局上（服务端写的是对的，只是界面错了），
  // 返回列表后 cur 置空还会直接抛 TypeError。
  const id = cur.id;
  const idx = step - 1;
  const btn = $('btn-rec-review');
  const label = $('btn-rec-review-label');
  reviewing = true;
  btn.disabled = true;
  label.textContent = '正在讲解…（最长约 1 分钟）';
  sfx.play('button');
  try {
    const out = await reviewRecord(id, idx, store.llm);
    if (!cur || cur.id !== id) { reviewing = false; return; }
    cur.reviews = cur.reviews || {};
    cur.reviews[String(idx)] = out.text;
    setReviewText(cur, step);
    sfx.play('star');
  } catch (e) {
    toast('讲解失败：' + e.message, true, 4200);
    if (cur && cur.id === id) setReviewText(cur, step);
  } finally {
    reviewing = false;
    btn.disabled = false;
    if (!(cur.reviews || {})[String(idx)]) label.textContent = '问 AI 讲解这一手';
  }
}

function showList() {
  cur = null;
  reviewing = false;
  $('record-detail').hidden = true;
  $('record-list').hidden = false;
  // 列表里可能已有多处讲解/分析标记，回来时刷一遍（同页数据已变）
  loadPage(0);
}
