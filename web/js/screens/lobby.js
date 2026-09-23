// 大厅：模式入口与开局设置弹窗。
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { toast, showScreen } from '../ui.js';
import { api } from '../net.js';
import { showPuzzles } from './puzzles.js';

const $ = (id) => document.getElementById(id);

// 等待中的元素：禁用点击并挂一个小转环（创建对局有网络往返，空等会让人以为没点上）
function setBusy(el, on) {
  if (!el) return;
  el.classList.toggle('busy', on);
  const old = el.querySelector('.busy-ring');
  if (on && !old) {
    const r = document.createElement('span');
    r.className = 'ring busy-ring';
    el.appendChild(r);
  } else if (!on) {
    old?.remove();
  }
}

const levelNames = [
  [1, '入门'], [2, '初学'], [3, '业余初级'], [4, '业余三级'],
  [5, '业余五级'], [6, '业余七级'], [7, '业余九级'], [8, '县市级'],
  [9, '市冠军'], [10, '省冠军'], [11, '省强手'], [12, '国家大师'],
  [13, '国家强手'], [14, '特大水准'], [15, '特大强棋'], [16, '特大全力'],
];

export function initLobby(onStart) {
  document.querySelectorAll('.mode-card').forEach((card) => {
    card.onclick = async () => {
      sfx.play('button');
      const mode = card.dataset.mode;
      if (mode === 'puzzle') {
        // 进列表前刷新：自定义残局是全服共享的，刚保存/别人新存的要能立刻看到
        await showPuzzles();
        return;
      }
      if (mode === 'custom') {
        // 自定义残局：进摆局屏自己摆，摆好保存后按 id 走与内置残局相同的挑战链路。
        showScreen('setup');
        return;
      }
      if (mode === 'pony') {
        // 小马冲冲：独立单页小游戏（共享主题/音效设置，顶部可返回大厅）
        location.href = 'pony-rush.html';
        return;
      }
      if (mode === 'local_2p') {
        setBusy(card, true);
        try {
          await onStart('local_2p', {});
        } finally {
          setBusy(card, false);
        }
        return;
      }
      openSetup(mode, onStart);
    };
  });
}

function openSetup(mode, onStart) {
  const isLLM = mode === 'llm';
  if (isLLM && !store.llm.model) {
    toast('首次使用大模型对弈，请先在右上角设置中填写 API 配置', true, 4200);
    document.getElementById('settings-overlay').hidden = false;
    return;
  }
  const overlay = $('setup-overlay');
  const card = $('setup-card');
  card.innerHTML = `
    <div class="settings-head"><h2>${isLLM ? '大模型对弈' : '人机对战'}</h2></div>
    <section class="settings-section">
      <h3>你的执子</h3>
      <div class="settings-row side-pick">
        <button class="ctl-btn side-btn active" data-side="red">执红先行</button>
        <button class="ctl-btn side-btn" data-side="black">执黑后行</button>
      </div>
      ${isLLM ? `
      <p class="setup-model">模型：<b>${store.llm.model}</b>（可在设置中修改）</p>` : `
      <h3>AI 难度（16 档）</h3>
      <div class="level-grid">
        ${levelNames.map(([v, n]) => `<button class="ctl-btn lv-btn${v === 4 ? ' active' : ''}" data-lv="${v}">${v}·${n}</button>`).join('')}
      </div>`}
    </section>
    <div class="settings-row" style="justify-content:flex-end;margin-top:8px">
      <button class="ctl-btn" id="setup-cancel">取消</button>
      <button class="ctl-btn primary" id="setup-ok" style="border-color:var(--gold-soft);color:var(--gold)">开始对局</button>
    </div>
  `;
  overlay.hidden = false;

  let side = 'red', level = 4;
  card.querySelectorAll('.side-btn').forEach((b) => {
    b.onclick = () => {
      card.querySelectorAll('.side-btn').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      side = b.dataset.side;
      sfx.play('button');
    };
  });
  card.querySelectorAll('.lv-btn').forEach((b) => {
    b.onclick = () => {
      card.querySelectorAll('.lv-btn').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      level = +b.dataset.lv;
      sfx.play('button');
    };
  });
  $('setup-cancel').onclick = () => { overlay.hidden = true; };
  $('setup-ok').onclick = async () => {
    sfx.play('button');
    const okBtn = $('setup-ok');
    setBusy(okBtn, true);
    try {
      // 保持弹窗开着显示转环，直到真正进入对局（或失败）再收起
      await onStart(mode, { side, level });
    } finally {
      setBusy(okBtn, false);
      overlay.hidden = true;
    }
  };
}
