// localStorage 偏好存储：大模型配置、残局进度、主题、音效。
const KEY = 'qijing.v1';

const defaults = {
  llm: {
    baseURL: 'https://api.deepseek.com/v1',
    apiKey: '',
    model: '',
    temperature: 0.3,
    timeoutMs: 30000,
    includeLegalMoves: true,
  },
  puzzleStars: {},   // id -> 星数
  theme: { pieces: 'wood', board: 'maple', particles: true },
  sound: true,
};

let data;
try {
  data = Object.assign({}, defaults, JSON.parse(localStorage.getItem(KEY) || '{}'));
} catch { data = { ...defaults }; }
data.llm = Object.assign({}, defaults.llm, data.llm);
data.theme = Object.assign({}, defaults.theme, data.theme);
// 布尔项规范化：旧数据缺省时按默认开启（避免 undefined 被序列化丢失导致后端误判关闭）
data.llm.engineAssist = data.llm.engineAssist !== false;
data.llm.includeLegalMoves = data.llm.includeLegalMoves !== false;

function save() {
  try { localStorage.setItem(KEY, JSON.stringify(data)); } catch { /* 忽略配额 */ }
}

export const store = {
  get llm() { return data.llm; },
  setLLM(cfg) { data.llm = Object.assign({}, data.llm, cfg); save(); },
  get theme() { return data.theme; },
  setTheme(t) { data.theme = Object.assign({}, data.theme, t); save(); },
  get sound() { return data.sound; },
  setSound(v) { data.sound = v; save(); },
  stars(id) { return data.puzzleStars[id] || 0; },
  setStars(id, n) {
    if ((data.puzzleStars[id] || 0) >= n) return;
    data.puzzleStars[id] = n;
    save();
  },
};
