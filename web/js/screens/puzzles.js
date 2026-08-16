// 残局浏览：分级筛选、星级展示、进入挑战。
import { listPuzzles } from '../net.js';
import { store } from '../store.js';
import { sfx } from '../audio.js';

const $ = (id) => document.getElementById(id);

let onStart = null;
let all = [];

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

function render(diff) {
  const grid = $('puzzle-grid');
  grid.innerHTML = '';
  const list = diff ? all.filter((p) => p.difficulty === diff) : all;
  for (const p of list) {
    const card = document.createElement('button');
    card.className = 'puzzle-card';
    card.innerHTML = `
      <div class="puzzle-name">${p.name}</div>
      <div class="puzzle-meta">
        <span class="puzzle-goal-tag ${p.goal}">${p.goal === 'win' ? '红先胜' : '红先和'}</span>
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
