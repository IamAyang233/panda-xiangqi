// REST 与 WebSocket 客户端。
import { BASE } from './base.js';

// apiURL 把以 "/" 开头的接口路径拼到网关基地址 BASE 上。
export const apiURL = (p) => BASE + (p.startsWith('/') ? p.slice(1) : p);

export async function api(path, body) {
  const opt = body !== undefined
    ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
    : {};
  const resp = await fetch(apiURL(path), opt);
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    try { msg = (await resp.json()).error || msg; } catch { /* 保留默认 */ }
    throw new Error(msg);
  }
  return resp.json();
}

export async function createGame(payload) {
  return api('/api/games', payload);
}

export async function listPuzzles(difficulty) {
  const q = difficulty ? `?difficulty=${encodeURIComponent(difficulty)}` : '';
  return api('/api/puzzles' + q);
}

export async function validateLLM(cfg) {
  return api('/api/llm/validate', cfg);
}

// GameConn 一局对局的 WS 封装：请求-回应式监听。
export class GameConn {
  constructor(gameId) {
    this.gameId = gameId;
    this.handlers = new Map(); // type -> [fn]
    this.anyHandlers = [];
    this.pendingLegal = null;
    this.ws = null;
    this.closed = false;        // 是否已终态关闭（手动或已放弃）
    this.manualClose = false;   // 主动调用 close()：不再自动重连
    this.reconnectAttempts = 0;
    this.reconnectTimer = null;
    this.onReconnecting = null; // (attempt:number) => void  正在重连（可提示 UI）
    this.onReconnected = null;  // () => void  重连成功（可清理临时选中态）
  }

  connect() {
    return new Promise((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      this.ws = new WebSocket(`${proto}://${location.host}${BASE}api/ws?gameId=${this.gameId}`);
      this.ws.onmessage = (ev) => {
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === 'legal_moves' && this.pendingLegal) {
          const cb = this.pendingLegal;
          this.pendingLegal = null;
          cb(msg.targets || []);
          return;
        }
        for (const fn of this.anyHandlers) fn(msg);
        const list = this.handlers.get(msg.type);
        if (list) for (const fn of list) fn(msg);
      };
      this.ws.onopen = () => {
        this.reconnectAttempts = 0;
        this.closed = false;
        resolve();
      };
      this.ws.onerror = () => { /* 错误最终由 onclose 兜底处理 */ };
      this.ws.onclose = () => {
        this.closed = true;
        if (this.manualClose) return; // 主动关闭：不重连
        this._scheduleReconnect();
      };
    });
  }

  // 意外断开后指数退避自动重连（1s→2s→4s→8s→16s，封顶 15s）。
  // 重连无需新建局：服务端 Join 会回送完整 state，前端据此全量重建棋盘。
  _scheduleReconnect() {
    if (this.manualClose) return;
    this.reconnectAttempts++;
    const delay = Math.min(1000 * 2 ** Math.min(this.reconnectAttempts - 1, 4), 15000);
    if (this.onReconnecting) this.onReconnecting(this.reconnectAttempts);
    clearTimeout(this.reconnectTimer);
    this.reconnectTimer = setTimeout(() => {
      this.connect()
        .then(() => { if (this.onReconnected) this.onReconnected(); })
        .catch(() => { /* 连接失败：onclose 会再次触发重连 */ });
    }, delay);
  }

  on(type, fn) {
    if (!this.handlers.has(type)) this.handlers.set(type, []);
    this.handlers.get(type).push(fn);
  }

  onAny(fn) { this.anyHandlers.push(fn); }

  send(obj) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(obj));
  }

  sendMove(from, to) { this.send({ type: 'move', from, to }); }

  // legalTargets 查询某子合法落点（规则单一事实来源在后端）。
  legalTargets(from) {
    return new Promise((resolve) => {
      this.pendingLegal = resolve;
      this.send({ type: 'legal', from });
      setTimeout(() => {
        if (this.pendingLegal === resolve) { this.pendingLegal = null; resolve([]); }
      }, 3000);
    });
  }

  undo() { this.send({ type: 'undo' }); }
  hint() { this.send({ type: 'hint' }); }
  resign() { this.send({ type: 'resign' }); }
  restart() { this.send({ type: 'restart' }); }

  close() {
    this.manualClose = true;
    this.closed = true;
    clearTimeout(this.reconnectTimer);
    if (this.ws) try { this.ws.close(); } catch { /* 已关闭 */ }
    this.ws = null;
  }
}
