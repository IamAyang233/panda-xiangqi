// Web Audio 合成音效：落子/吃子/将军/胜负/按钮/星级（无外部音频资源，零下载）。
import { store } from './store.js';

let ctx = null;

function ac() {
  if (!ctx) {
    ctx = new (window.AudioContext || window.webkitAudioContext)();
  }
  if (ctx.state === 'suspended') ctx.resume();
  return ctx;
}

function env(gain, t0, attack, peak, decay) {
  gain.gain.setValueAtTime(0.0001, t0);
  gain.gain.linearRampToValueAtTime(peak, t0 + attack);
  gain.gain.exponentialRampToValueAtTime(0.0001, t0 + attack + decay);
}

// tone 单音：type 波形 / freq 频率 / dur 时长 / peak 音量
function tone({ type = 'sine', freq = 440, dur = 0.2, peak = 0.25, delay = 0, sweep = 0 }) {
  const c = ac();
  const t0 = c.currentTime + delay;
  const osc = c.createOscillator();
  const g = c.createGain();
  osc.type = type;
  osc.frequency.setValueAtTime(freq, t0);
  if (sweep) osc.frequency.exponentialRampToValueAtTime(Math.max(40, freq + sweep), t0 + dur);
  env(g, t0, 0.008, peak, dur);
  osc.connect(g).connect(c.destination);
  osc.start(t0);
  osc.stop(t0 + dur + 0.05);
}

// knock 敲击声：短促方波 + 快速衰减（模拟木质落子"啪"）
function knock(freq = 720, dur = 0.07, peak = 0.5) {
  tone({ type: 'square', freq, dur, peak, sweep: -freq * 0.4 });
  tone({ type: 'sine', freq: freq / 2, dur: dur * 1.4, peak: peak * 0.7 });
}

// noiseBurst 噪声爆发（碎裂感）
function noiseBurst(dur = 0.25, peak = 0.3) {
  const c = ac();
  const len = Math.floor(c.sampleRate * dur);
  const buf = c.createBuffer(1, len, c.sampleRate);
  const ch = buf.getChannelData(0);
  for (let i = 0; i < len; i++) ch[i] = (Math.random() * 2 - 1) * (1 - i / len) ** 2;
  const src = c.createBufferSource();
  src.buffer = buf;
  const g = c.createGain();
  g.gain.value = peak;
  const filter = c.createBiquadFilter();
  filter.type = 'lowpass';
  filter.frequency.value = 1400;
  src.connect(filter).connect(g).connect(c.destination);
  src.start();
}

export const sfx = {
  play(name) {
    if (!store.sound) return;
    try { this['_' + name](); } catch { /* 音频不可用时静默 */ }
  },
  _move() { knock(760, 0.06, 0.45); },
  _capture() { knock(420, 0.1, 0.6); noiseBurst(0.22, 0.25); },
  _check() {
    tone({ type: 'sine', freq: 196, dur: 1.1, peak: 0.3 });
    tone({ type: 'sine', freq: 294, dur: 0.9, peak: 0.2, delay: 0.02 });
    tone({ type: 'triangle', freq: 392, dur: 0.5, peak: 0.12, delay: 0.05 });
  },
  _win() {
    [523, 659, 784, 1047].forEach((f, i) =>
      tone({ type: 'triangle', freq: f, dur: 0.35, peak: 0.22, delay: i * 0.12 }));
  },
  _lose() {
    [392, 330, 262, 196].forEach((f, i) =>
      tone({ type: 'sine', freq: f, dur: 0.4, peak: 0.2, delay: i * 0.16 }));
  },
  _draw() {
    tone({ type: 'sine', freq: 440, dur: 0.3, peak: 0.2 });
    tone({ type: 'sine', freq: 440, dur: 0.45, peak: 0.18, delay: 0.35 });
  },
  _button() { tone({ type: 'square', freq: 980, dur: 0.03, peak: 0.12 }); },
  _illegal() { tone({ type: 'square', freq: 160, dur: 0.12, peak: 0.25 }); },
  _star() {
    [880, 1174, 1568].forEach((f, i) =>
      tone({ type: 'sine', freq: f, dur: 0.3, peak: 0.2, delay: i * 0.14 }));
  },
};
