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
    this.closed = false;
  }

  connect() {
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
    this.ws.onclose = () => { this.closed = true; };
    return new Promise((resolve, reject) => {
      this.ws.onopen = resolve;
      this.ws.onerror = reject;
    });
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
    this.closed = true;
    if (this.ws) try { this.ws.close(); } catch { /* 已关闭 */ }
  }
}
