// 大厅：模式入口与开局设置弹窗。
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { toast, showScreen } from '../ui.js';
import { api } from '../net.js';

const $ = (id) => document.getElementById(id);

const levelNames = [
  [1, '入门'], [2, '初学'], [3, '业余初级'], [4, '业余三级'],
  [5, '业余五级'], [6, '业余七级'], [7, '业余九级'], [8, '县市级'],
  [9, '市冠军'], [10, '省冠军'], [11, '省强手'], [12, '国家大师'],
  [13, '国家强手'], [14, '特大水准'], [15, '特大强棋'], [16, '特大全力'],
];

export function initLobby(onStart) {
  document.querySelectorAll('.mode-card').forEach((card) => {
    card.onclick = () => {
      sfx.play('button');
      const mode = card.dataset.mode;
      if (mode === 'puzzle') {
        showScreen('puzzles');
        return;
      }
      if (mode === 'local_2p') {
        onStart('local_2p', {});
        return;
      }
      openSetup(mode, onStart);
    };
  });
  loadEngineInfo();
}

// loadEngineInfo 拉取 /api/status，把真实引擎名称与可用状态显示在大厅底部，
// 取代原先写死的“内置皮卡鱼”文案——避免与运行时实际可用性不符。
async function loadEngineInfo() {
  const el = document.getElementById('engine-info');
  if (!el) return;
  try {
    const st = await api('/api/status');
    if (st.uciAvailable) {
      el.className = 'ok';
      el.innerHTML = `引擎：<b>${st.engine || 'Pikafish'}</b> · 已内置可用`;
    } else {
      el.className = 'warn';
      el.textContent = '引擎：自研算法（皮卡鱼未启用，已自动兜底）';
    }
  } catch {
    el.className = 'err';
    el.textContent = '引擎状态获取失败';
  }
}

function openSetup(mode, onStart) {
  const isLLM = mode === 'llm';
  if (isLLM && !store.llm.model) {
    toast('首次使用大模型对弈，请先在右上角 ⚙️ 设置中填写 API 配置', true, 4200);
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
  $('setup-ok').onclick = () => {
    sfx.play('button');
    overlay.hidden = true;
    onStart(mode, { side, level });
  };
}
