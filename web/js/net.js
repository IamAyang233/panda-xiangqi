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

// ---- 心跳与死连接检测 ----
// 背景：应用经 fnOS 网关反代访问，网关对空闲连接有读超时（nginx 类默认约 60s）。
// 下棋存在大量无消息时段（读讲解/思考/切后台），连接一旦空闲就被网关掐掉，
// 用户看到的就是"莫名其妙断开"。对策：每 25s 发一次应用层 ping（<60s，留余量），
// 让网关始终看到流量。
const HB_INTERVAL = 25000;
// 连续多个心跳周期收不到任何回包（pong 或其它消息）→ 判定 TCP 半开，主动断开走重连。
// 比被动等 onclose 快得多：半开连接浏览器往往几分钟都察觉不到。
const HB_STALE = 60000;

// 心跳定时器必须放 Web Worker：后台标签页的主线程 setInterval 会被浏览器节流到
// ≥1 分钟，页面切后台就发不出心跳——那正是"切到别的窗口回来就断线"的原因。
// Worker 的定时器不受页面节流影响。用 Blob 内联，免得在网关前缀下多管一个文件路径。
const hbWorkerSrc = (ms) =>
  'let t=null;onmessage=e=>{' +
  'if(e.data==="start"&&!t)t=setInterval(()=>postMessage(0),' + ms + ');' +
  'if(e.data==="stop"&&t){clearInterval(t);t=null;}}';

// GameConn 一局对局的 WS 封装：请求-回应式监听。
export class GameConn {
  // opts.heartbeatMs / opts.heartbeatStaleMs：测试时可调短；默认 25s / 60s
  constructor(gameId, opts = {}) {
    this.gameId = gameId;
    this.hbInterval = opts.heartbeatMs || HB_INTERVAL;
    this.hbStale = opts.heartbeatStaleMs || HB_STALE;
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
    this.outbox = [];           // 断开窗口内待发操作，重连成功后按序重放
    this.lastPongAt = 0;        // 最近一次收到任何回包的时间（心跳死连接判据）
    this.hbWorker = null;       // 心跳 Worker；不可用时退回主线程定时器
    this.hbTimer = null;
  }

  connect() {
    return new Promise((resolve, reject) => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      this.ws = new WebSocket(`${proto}://${location.host}${BASE}api/ws?gameId=${this.gameId}`);
      this.ws.onmessage = (ev) => {
        this.lastPongAt = Date.now();   // 任何回包都证明连接活着（含 pong / state / error）
        let msg;
        try { msg = JSON.parse(ev.data); } catch { return; }
        if (msg.type === 'legal_moves' && this.pendingLegal) {
          const pending = this.pendingLegal;
          // ⚠️ 用 from 配对：不匹配就是**过期回应**（用户已经点了别的子），丢掉即可，
          // 由新请求自己的回应来收尾。少了这一句，快速连点两颗子时第一颗的落点会
          // 落到第二颗的 resolver 上，第二颗显示错误的落点且再也纠正不过来
          // （它自己的回应到达时 pendingLegal 已是 null，被当成普通消息丢弃）。
          if (pending.from !== msg.from) return;
          this.pendingLegal = null;
          pending.resolve(msg.targets || []);
          return;
        }
        for (const fn of this.anyHandlers) fn(msg);
        const list = this.handlers.get(msg.type);
        if (list) for (const fn of list) fn(msg);
      };
      this.ws.onopen = () => {
        this.reconnectAttempts = 0;
        this.closed = false;
        this._startHeartbeat();
        this._flushOutbox();
        resolve();
      };
      this.ws.onerror = () => { /* 错误最终由 onclose 兜底处理 */ };
      this.ws.onclose = () => {
        this.closed = true;
        this._stopHeartbeat();
        // ⚠️ 必须在这里让 Promise 落地。原来只在 onopen 里 resolve、别处都不 settle，
        // 于是「连不上」时 `await conn.connect()` **永久悬挂** —— 两个调用点的
        // try/catch 全成了死代码，界面会一直停在载入遮罩上（catch 里本来会清遮罩、
        // 提示「连接对局服务失败」、会话过期时回大厅）。已经 resolve 过再 reject 是空操作。
        reject(new Error('WebSocket 未建立即断开'));
        if (this.manualClose) return;
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

  // ---- 心跳 ----
  _startHeartbeat() {
    this._stopHeartbeat();
    this.lastPongAt = Date.now();
    const tick = () => this._ping();
    if (typeof Worker !== 'undefined') {
      try {
        const url = URL.createObjectURL(new Blob([hbWorkerSrc(this.hbInterval)], { type: 'text/javascript' }));
        this.hbWorker = new Worker(url);
        this.hbWorker.onmessage = tick;
        this.hbWorker.postMessage('start');
        return;
      } catch { /* Worker 不可用（极旧浏览器/受限环境）→ 主线程兜底 */ }
    }
    this.hbTimer = setInterval(tick, this.hbInterval);
  }

  _stopHeartbeat() {
    if (this.hbWorker) {
      try { this.hbWorker.postMessage('stop'); this.hbWorker.terminate(); } catch { /* 已终止 */ }
      this.hbWorker = null;
    }
    if (this.hbTimer) { clearInterval(this.hbTimer); this.hbTimer = null; }
  }

  _ping() {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    // 死连接检测：远超一个周期没有任何回包 → 半开连接，主动断开走重连
    if (Date.now() - this.lastPongAt > this.hbStale) {
      try { this.ws.close(); } catch { /* 已关闭 */ }
      return;
    }
    this.send({ type: 'ping' });
  }

  on(type, fn) {
    if (!this.handlers.has(type)) this.handlers.set(type, []);
    this.handlers.get(type).push(fn);
  }

  onAny(fn) { this.anyHandlers.push(fn); }

  send(obj) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(obj));
      return;
    }
    // 断开窗口内的操作不能静默丢弃（用户点了棋却毫无反应）：
    // 入队，重连成功后按序重放。心跳不走这里（_ping 已先判 OPEN）。
    if (this.manualClose) return;
    if (this.outbox.length < 20) this.outbox.push(obj);
  }

  _flushOutbox() {
    if (!this.outbox.length) return;
    const q = this.outbox;
    this.outbox = [];
    for (const o of q) this.send(o);
  }

  sendMove(from, to) { this.send({ type: 'move', from, to }); }

  // legalTargets 查询某子合法落点（规则单一事实来源在后端）。
  //
  // ⚠️ 这个 Promise 必须**保证落地**，而且必须按「本次调用」记状态，不能用共享槽位
  // 判断：槽位会被后一次调用覆盖、也会被回应消费掉，用 `pendingLegal === resolve`
  // 之类的条件判超时，会让被覆盖/已被消费的那个 Promise **永久悬挂**。
  // 这里用本地 done 标志收尾（回应与 3s 超时谁先到都行），槽位只是「当前该由谁接回应」。
  legalTargets(from) {
    return new Promise((resolve) => {
      let done = false;
      const finish = (targets) => {
        if (done) return;
        done = true;
        if (this.pendingLegal && this.pendingLegal.resolve === finish) this.pendingLegal = null;
        resolve(targets);
      };
      this.pendingLegal = { from, resolve: finish };
      this.send({ type: 'legal', from });
      setTimeout(() => finish([]), 3000);
    });
  }

  undo() { this.send({ type: 'undo' }); }
  hint() { this.send({ type: 'hint' }); }
  resign() { this.send({ type: 'resign' }); }
  restart() { this.send({ type: 'restart' }); }

  close() {
    this.manualClose = true;
    this.closed = true;
    this._stopHeartbeat();
    clearTimeout(this.reconnectTimer);
    if (this.ws) try { this.ws.close(); } catch { /* 已关闭 */ }
    this.ws = null;
  }
}
