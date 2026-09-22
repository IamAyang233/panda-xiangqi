// localStorage 偏好存储：大模型配置、残局进度、主题、音效。
const KEY = 'qijing.v1';
// 当前进行中对局（断网/刷新恢复用）。仅「在局中」时存在，返回大厅即清除。
const CURRENT_KEY = 'qijing.game.v1';

const defaults = {
  llm: {
    baseURL: 'https://api.deepseek.com/v1',
    apiKey: '',
    model: '',
    temperature: 0.3,
    // 180s：推理模型（思考型）单步实测 50~90s；而「截断→加倍重试」是两次请求共用
    // 这个上限，所以必须留出跑两次的余地。
    timeoutMs: 180000,
    // 单次回复 token 上限。推理模型会先把额度花在思考上，给小了正文为空。
    maxTokens: 8192,
    includeLegalMoves: true,
  },
  puzzleStars: {},   // id -> 星数
  theme: { pieces: 'wood', board: 'maple', particles: true },
  sound: true,
};

// 我们随包发出过的超时默认值（30s → 120s → 180s）。存量配置里若仍是这些
// 「我们自己发出去的」值，视为用户未曾调整过，升到当前默认；用户改过的其它值不动。
const LEGACY_DEFAULT_TIMEOUTS = [30000, 120000];

let data;
try {
  data = Object.assign({}, defaults, JSON.parse(localStorage.getItem(KEY) || '{}'));
} catch { data = { ...defaults }; }
data.llm = Object.assign({}, defaults.llm, data.llm);
data.theme = Object.assign({}, defaults.theme, data.theme);
if (LEGACY_DEFAULT_TIMEOUTS.includes(data.llm.timeoutMs)) {
  data.llm.timeoutMs = defaults.llm.timeoutMs;
}
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

  // 当前对局存档：断网/刷新后凭 gameId 重连恢复。
  // 结构 { gameId, mode, youSide, side, level, opts }；仅比赛进行中写入。
  get currentGame() {
    try { return JSON.parse(localStorage.getItem(CURRENT_KEY) || 'null'); } catch { return null; }
  },
  saveCurrentGame(g) {
    try { localStorage.setItem(CURRENT_KEY, JSON.stringify(g)); } catch { /* 忽略配额 */ }
  },
  clearCurrentGame() {
    try { localStorage.removeItem(CURRENT_KEY); } catch { /* 忽略 */ }
  },
};
