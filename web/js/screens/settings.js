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
  $('llm-temp').value = llm.temperature ?? 0.3;
  $('llm-timeout').value = llm.timeoutMs || 30000;
  $('llm-legal').checked = llm.includeLegalMoves !== false;
  $('llm-assist').checked = llm.engineAssist !== false;
  $('theme-pieces').value = store.theme.pieces;
  $('theme-board').value = store.theme.board;
  $('theme-particles').checked = store.theme.particles;

  const saveLLM = () => store.setLLM({
    baseURL: $('llm-baseurl').value.trim(),
    apiKey: $('llm-apikey').value.trim(),
    model: $('llm-model').value.trim(),
    temperature: parseFloat($('llm-temp').value) || 0.3,
    timeoutMs: +$('llm-timeout').value || 30000,
    includeLegalMoves: $('llm-legal').checked,
    engineAssist: $('llm-assist').checked,
  });

  for (const id of ['llm-baseurl', 'llm-apikey', 'llm-model', 'llm-temp', 'llm-timeout', 'llm-legal', 'llm-assist']) {
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
