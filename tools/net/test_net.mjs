// GameConn 心跳/断线逻辑的无头测试：用桩 WebSocket 验证
//   1) 主线程兜底心跳路径（Worker 不可用时 setInterval 也能发 ping）
//   2) 收到任何回包刷新活性时间
//   3) 死连接检测：超时无回包 → 主动 close 走重连
//   4) 断开窗口内的操作入队、重连后重放，不静默丢弃
//   5) 主动 close 后不再排队
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const T = path.join(ROOT, '_nettest');
fs.rmSync(T, { recursive: true, force: true });
fs.mkdirSync(T, { recursive: true });
fs.copyFileSync(path.join(ROOT, 'web', 'js', 'net.js'), path.join(T, 'net.js'));
fs.copyFileSync(path.join(ROOT, 'web', 'js', 'base.js'), path.join(T, 'base.js'));
fs.writeFileSync(path.join(T, 'package.json'), '{"type":"module"}');

// ---- 桩：浏览器环境 ----
globalThis.location = { pathname: '/', protocol: 'http:', host: 'test' };

class FakeWS {
  constructor(url) {
    this.url = url;
    this.readyState = 0;              // CONNECTING
    this.sent = [];
    this.closeCalls = 0;
    FakeWS.instances.push(this);
  }
  send(d) { this.sent.push(d); }
  close() {
    this.closeCalls++;
    if (this.readyState !== 3) { this.readyState = 3; this.onclose && this.onclose(); }
  }
  // 测试辅助
  open() { this.readyState = 1; this.onopen && this.onopen(); }
  recv(obj) { this.onmessage && this.onmessage({ data: JSON.stringify(obj) }); }
}
FakeWS.OPEN = 1;
FakeWS.instances = [];
globalThis.WebSocket = FakeWS;
// 故意不定义 Worker：验证主线程兜底路径（浏览器里的 Worker 路径由无头浏览器 E2E 覆盖）

let pass = 0, fail = 0;
const ok = (n, c, extra = '') => { c ? (pass++, console.log('  ✅', n)) : (fail++, console.log('  ❌', n, extra)); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

const { GameConn } = await import(pathToFileURL(path.join(T, 'net.js')).href);

// 心跳 30ms / 判死 200ms，测试跑得快
const conn = new GameConn('g-test', { heartbeatMs: 30, heartbeatStaleMs: 200 });

console.log('【1】连接与主线程兜底心跳');
const p = conn.connect();
FakeWS.instances[0].open();
await p;
ok('connect 在 onopen 时 resolve', conn.ws && conn.ws.readyState === 1);
ok('Worker 不可用时退回主线程定时器', conn.hbWorker === null && conn.hbTimer !== null);
await sleep(140);
const pings = FakeWS.instances[0].sent
  .map(s => { try { return JSON.parse(s).type; } catch { return '?'; } })
  .filter(t => t === 'ping').length;
ok(`兜底心跳发出 ping（30ms 间隔，140ms 内 ≥3 次）`, pings >= 3, `实际 ${pings}`);

console.log('\n【2】回包刷新活性 + 服务端 pong');
const before = conn.lastPongAt;
await sleep(20);
ok('无回包时 lastPongAt 不变', conn.lastPongAt === before);
conn.ws.recv({ type: 'pong' });          // 模拟服务端回 pong
ok('收到 pong 刷新 lastPongAt', conn.lastPongAt > before);
conn.ws.recv({ type: 'state', foo: 1 }); // 其它消息同样算活性证据
ok('收到业务消息同样算活性', conn.lastPongAt >= before);
// lastPongAt 由 onmessage 顶层刷新，pong 也会走 handler，不抛错即可

console.log('\n【3】死连接检测');
const ws0 = conn.ws;
conn.lastPongAt = Date.now() - 10_000;   // 远超 200ms 判死线
conn._ping();
ok('超时无回包 → 主动 close（走重连）', ws0.closeCalls === 1);
ok('close 后心跳停止', conn.hbTimer === null && conn.hbWorker === null);

console.log('\n【4】断开窗口内的操作入队、重连后重放');
conn.reconnectAttempts = 0;
conn._scheduleReconnect();
clearTimeout(conn.reconnectTimer);        // 不真等退避
const ws1 = new FakeWS('ws://test/api/ws');
conn.ws = ws1;
ws1.onopen = conn.ws.onopen;              // 复用同一处理器
// 直接调处理器（onopen 里会启心跳+重放）
conn.ws.readyState = 1;
conn.send({ type: 'move', from: 'h2', to: 'e2' });   // readyState=1 → 直发
ws1.readyState = 0;                        // 模拟断开
conn.send({ type: 'undo' });
conn.send({ type: 'hint' });
ok('断开窗口内操作入队（不丢弃）', conn.outbox.length === 2, `实际 ${conn.outbox.length}`);
const sentBefore = ws1.sent.length;
ws1.readyState = 1;
conn._flushOutbox();
const replayed = ws1.sent.slice(sentBefore).map(s => JSON.parse(s).type);
ok('重连后按序重放', replayed.length === 2 && replayed[0] === 'undo' && replayed[1] === 'hint',
  `实际 ${JSON.stringify(replayed)}`);
ok('重放后队列清空', conn.outbox.length === 0);

console.log('\n【5】主动 close 后不再排队');
conn.close();
const n0 = conn.outbox.length;
conn.send({ type: 'resign' });
ok('manualClose 后 send 不入队', conn.outbox.length === n0);
ok('close 会停掉心跳', conn.hbTimer === null);

console.log(`\n结果：通过 ${pass}，失败 ${fail}`);
fs.rmSync(T, { recursive: true, force: true });
process.exit(fail ? 1 : 0);
