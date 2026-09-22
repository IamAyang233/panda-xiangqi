// 设置页：大模型配置 / 主题 / 音效。
import { store } from '../store.js';
import { sfx } from '../audio.js';
import { validateLLM } from '../net.js';
import { toast } from '../ui.js';

const $ = (id) => document.getElementById(id);

export function initSettings(onThemeChange) {
  const llm = store.llm;
  $('llm-baseurl').value = llm.baseURL || '';
  $('llm-apikey').value = llm.apiKey || '';
  $('llm-model').value = llm.model || '';
  // 温度：null 表示「用户主动清空 = 不发送该参数」，此时必须回显为空。
  // ⚠️ 不能用 `?? 0.3` 兜底：那会把清空状态显示成 0.3，用户改别的字段时又把它写回去，
  // 「不发送温度」这个状态就丢了（o1/o3 系会因此每步 400）。
  $('llm-temp').value = (llm.temperature === null || llm.temperature === undefined) ? '' : llm.temperature;
  // 兜底值必须与 store.js 的默认一致（180s）：推理模型单步 50~90s，
  // 而「截断→加倍重试」两次共用这个上限。
  $('llm-timeout').value = llm.timeoutMs || 180000;
  $('llm-maxtokens').value = llm.maxTokens || 8192;
  $('llm-legal').checked = llm.includeLegalMoves !== false;
  $('llm-assist').checked = llm.engineAssist !== false;
  $('theme-pieces').value = store.theme.pieces;
  $('theme-board').value = store.theme.board;
  $('theme-particles').checked = store.theme.particles;

  const saveLLM = () => {
    // 温度留空 = 不发送该参数（null → 后端 pointer 为 nil）。
    // o1/o3 等只接受默认温度的模型必须能表达「不指定」，否则每步 400。
    const tempRaw = $('llm-temp').value.trim();
    store.setLLM({
      baseURL: $('llm-baseurl').value.trim(),
      apiKey: $('llm-apikey').value.trim(),
      model: $('llm-model').value.trim(),
      temperature: tempRaw === '' ? null : (parseFloat(tempRaw) || 0.3),
      // 留空时回落到 180s（与 store.js 默认一致），不要退回旧的 30s
      timeoutMs: +$('llm-timeout').value || 180000,
      maxTokens: +$('llm-maxtokens').value || 8192,
      includeLegalMoves: $('llm-legal').checked,
      engineAssist: $('llm-assist').checked,
    });
  };

  for (const id of ['llm-baseurl', 'llm-apikey', 'llm-model', 'llm-temp', 'llm-timeout', 'llm-maxtokens', 'llm-legal', 'llm-assist']) {
    $(id).addEventListener('change', saveLLM);
  }

  $('btn-llm-test').onclick = async () => {
    saveLLM();
    const out = $('llm-test-result');
    out.textContent = '测试中…';
    out.className = 'test-result';
    try {
      const r = await validateLLM(store.llm);
      if (r.ok) {
        out.textContent = `✓ ${r.message}（${r.latencyMs}ms）`;
        out.className = 'test-result ok';
        sfx.play('star');
      } else {
        out.textContent = `✗ ${r.message}`;
        out.className = 'test-result bad';
        sfx.play('illegal');
      }
    } catch (e) {
      out.textContent = `✗ ${e.message}`;
      out.className = 'test-result bad';
    }
  };

  $('theme-pieces').onchange = () => {
    store.setTheme({ pieces: $('theme-pieces').value });
    onThemeChange();
  };
  $('theme-board').onchange = () => {
    store.setTheme({ board: $('theme-board').value });
    onThemeChange();
  };
  $('theme-particles').onchange = () => {
    store.setTheme({ particles: $('theme-particles').checked });
    onThemeChange();
  };

  $('btn-settings').onclick = () => { $('settings-overlay').hidden = false; sfx.play('button'); };
  $('btn-settings-close').onclick = () => { $('settings-overlay').hidden = true; };
  $('settings-overlay').addEventListener('pointerdown', (e) => {
    if (e.target.id === 'settings-overlay') $('settings-overlay').hidden = true;
  });

  $('btn-sound').onclick = () => {
    store.setSound(!store.sound);
    updateSoundIcon();
    if (store.sound) sfx.play('button');
  };
  updateSoundIcon();
}

function updateSoundIcon() {
  const use = $('btn-sound').querySelector('use');
  if (use) use.setAttribute('href', store.sound ? '#i-sound' : '#i-mute');
}
