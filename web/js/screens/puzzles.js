// 残局浏览：分级筛选、星级展示、进入挑战。
import { listPuzzles } from '../net.js';
import { store } from '../store.js';
import { sfx } from '../audio.js';

const $ = (id) => document.getElementById(id);

let onStart = null;
let all = [];
let currentFilter = '';   // 当前难度筛选（'' = 全部），决定"上一关/下一关"的遍历范围

export async function initPuzzles(handler) {
  onStart = handler;
  document.querySelectorAll('#difficulty-tabs .tab').forEach((tab) => {
    tab.onclick = () => {
      sfx.play('button');
      document.querySelectorAll('#difficulty-tabs .tab').forEach((t) => t.classList.remove('active'));
      tab.classList.add('active');
      render(tab.dataset.diff);
    };
  });
  await refresh();
}

// 首次打开列表（列表还是空的）时给个载入反馈；已有卡片就不清空，
// 因为退出对局返回也会调 refresh()，那时清空会闪一下。
function showListLoading() {
  const grid = $('puzzle-grid');
  if (grid.children.length) return;
  grid.innerHTML = '<div class="list-loading"><span class="ring"></span>正在载入残局…</div>';
}

export async function refresh() {
  showListLoading();
  let err = null;
  try {
    all = await listPuzzles();
  } catch (e) {
    // ⚠️ 不能静默吞掉：网络故障/后端 500 与「这个难度真的没有残局」在 UI 上
    // 长得一模一样（都是空列表），用户只会以为没内容、不会想到重试。
    err = e;
    all = [];
  }
  const active = document.querySelector('#difficulty-tabs .tab.active');
  render(active ? active.dataset.diff : '', err);
}

function starsHTML(n) {
  // ⚠️ 星数必须先夹到 [0,3]：localStorage 里若是旧版本/被手工改过的脏数据
  // （>3 或 NaN），'☆'.repeat(3 - n) 会得到负数并抛 RangeError，
  // 而 render() 没有 try/catch ⇒ 整个残局列表会白屏。
  const stars = Number.isFinite(n) ? Math.max(0, Math.min(3, Math.floor(n))) : 0;
  if (!stars) return '<span class="puzzle-stars" style="opacity:.4">☆☆☆</span>';
  return `<span class="puzzle-stars">${'★'.repeat(stars)}${'☆'.repeat(3 - stars)}</span>`;
}

function goalLabel(p) {
  const side = p.playerSide === 'black' ? '黑先' : '红先';
  const aim = p.goal === 'win' ? '胜' : '和';
  return `${side}${aim}`;
}

function render(diff, loadError) {
  currentFilter = diff || '';
  const grid = $('puzzle-grid');
  grid.innerHTML = '';
  const list = diff ? all.filter((p) => p.difficulty === diff) : all;
  for (const p of list) {
    const card = document.createElement('button');
    card.className = 'puzzle-card';
    card.innerHTML = `
      <div class="puzzle-name">${p.name}</div>
      <div class="puzzle-meta">
        <span class="puzzle-goal-tag ${p.goal} ${p.playerSide}">${goalLabel(p)}</span>
        ${p.difficulty} · 最少 ${p.parMoves} 步
      </div>
      ${starsHTML(store.stars(p.id))}
    `;
    card.onclick = () => {
      sfx.play('button');
      onStart('puzzle', { puzzleId: p.id });
    };
    grid.appendChild(card);
  }
  if (loadError) {
    // 拉取失败与「真的没有该难度」必须可区分：前者要提示重试。
    grid.innerHTML =
      '<p style="color:var(--danger,#e06c75);padding:20px">残局列表加载失败，请检查网络或稍后重试。</p>';
  } else if (!list.length) {
    grid.innerHTML = '<p style="color:var(--text-dim);padding:20px">该级别暂无残局</p>';
  }
}

// visibleList 当前筛选下实际展示（也就是可逐关切换）的列表。
function visibleList() {
  return currentFilter ? all.filter((p) => p.difficulty === currentFilter) : all;
}

// siblingOf 取同一列表内相邻的残局（delta = ±1）；到边界返回 null。
// 不在当前列表内（例如从别处直接进入）时退回全集定位，保证按钮不至于永远失效。
export function siblingOf(id, delta) {
  const inList = (list) => {
    const i = list.findIndex((p) => p.id === id);
    if (i < 0) return undefined;
    const j = i + delta;
    return (j >= 0 && j < list.length) ? list[j] : null;
  };
  const r = inList(visibleList());
  if (r !== undefined) return r;
  return inList(all) !== undefined ? inList(all) : null;
}

// positionOf 返回 {index,total}（1 基），供对局页显示"第 N/M 关"。
export function positionOf(id) {
  let list = visibleList();
  let i = list.findIndex((p) => p.id === id);
  if (i < 0) { list = all; i = list.findIndex((p) => p.id === id); }
  return i < 0 ? null : { index: i + 1, total: list.length };
}

// nameOf 取残局名称（列表尚未加载时返回空串）。
export function nameOf(id) {
  return all.find((p) => p.id === id)?.name || '';
}
