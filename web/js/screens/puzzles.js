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

export async function refresh() {
  try {
    all = await listPuzzles();
  } catch {
    all = [];
  }
  const active = document.querySelector('#difficulty-tabs .tab.active');
  render(active ? active.dataset.diff : '');
}

function starsHTML(n) {
  if (!n) return '<span class="puzzle-stars" style="opacity:.4">☆☆☆</span>';
  return `<span class="puzzle-stars">${'★'.repeat(n)}${'☆'.repeat(3 - n)}</span>`;
}

function goalLabel(p) {
  const side = p.playerSide === 'black' ? '黑先' : '红先';
  const aim = p.goal === 'win' ? '胜' : '和';
  return `${side}${aim}`;
}

function render(diff) {
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
  if (!list.length) {
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
