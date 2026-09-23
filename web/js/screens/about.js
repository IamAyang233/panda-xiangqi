// 关于页：检测更新（PanDa 推送更新系统）+ 更新日志 + Bug 反馈。
// 版本逻辑遵循《PanDa 推送更新系统开发文档 §6.3》：pickVer / cmpVer 三段数值比较。
import { sfx } from '../audio.js';
import { BASE } from '../base.js';

const $ = (id) => document.getElementById(id);

// pickVer 从字符串中提取三段版本号（"v1.2.3-beta" → [1,2,3]）。
function pickVer(s) {
  const m = /(\d+)\.(\d+)\.(\d+)/.exec(String(s || ''));
  return m ? [+m[1], +m[2], +m[3]] : [0, 0, 0];
}

// cmpVer 按数值比较版本：1.0.9 < 1.0.67。
function cmpVer(a, b) {
  const va = pickVer(a), vb = pickVer(b);
  for (let i = 0; i < 3; i++) {
    if (va[i] !== vb[i]) return va[i] - vb[i];
  }
  return 0;
}

// safeURL 只放行 http/https 绝对地址，其余（含 javascript: / data: / blob:）返回空串。
//
// 用途：更新日志里的下载链接来自**远端更新服务**（可配置的 PanDa 地址），把它直接
// 赋给 `a.href` 时，`javascript:` 这类伪协议会在点击时于本页执行脚本 —— 同一个
// 响应里的其它字段都走了 textContent，唯独 href 是一条能带执行语义的通道。
export function safeURL(s) {
  if (!s) return '';
  try {
    const u = new URL(String(s), location.href);
    return (u.protocol === 'http:' || u.protocol === 'https:') ? u.href : '';
  } catch {
    return '';
  }
}

export function initAbout() {
  $('btn-about').onclick = openAbout;
  $('btn-about-lobby').onclick = openAbout;
  $('btn-check-update').onclick = () => { sfx.play('button'); checkUpdate(); };
  $('btn-feedback').onclick = submitFeedback;
}

function openAbout() {
  sfx.play('button');
  document.querySelectorAll('.screen').forEach((s) => { s.hidden = true; });
  $('screen-about').hidden = false;
  $('btn-home').hidden = false;
  checkUpdate(); // 打开即自动检测更新
}

// checkUpdate 检测 PanDa 推送更新系统的新版本，并渲染更新日志。
async function checkUpdate() {
  const status = $('update-status');
  const log = $('changelog');
  status.textContent = '检测中…';
  status.className = 'test-result';
  let data;
  try {
    const resp = await fetch(BASE + 'api/update');
    data = await resp.json();
  } catch (e) {
    data = { ok: false, message: '网络错误：' + e.message };
  }
  const self = data.selfVersion || '1.0.0';
  // 顺带把本机版本显示出来。此前这一格是写死的品牌字串，界面上没有任何地方能看到
  // 自己在跑哪一版，而「检查更新」只会说「已是最新版本」—— 装了哪个版本无从确认。
  // 取不到版本时宁可不显示，也不把用于比较的兜底值 1.0.0 当成真版本露给用户。
  $('about-version').textContent = data.selfVersion
    ? `PANDA XIANGQI · v${data.selfVersion}`
    : 'PANDA XIANGQI';

  const releases = Array.isArray(data.releases) ? data.releases : [];
  if (!data.ok) {
    status.textContent = `✗ ${data.message || '更新服务响应异常'}`;
    status.className = 'test-result bad';
    renderChangelog([], log);
    return;
  }
  if (!releases.length) {
    status.textContent = '暂无版本记录（请稍后再试）';
    status.className = 'test-result';
    renderChangelog([], log);
    return;
  }
  const latest = String(releases[0].version || '');
  if (cmpVer(latest, self) > 0) {
    status.textContent = `发现新版本 v${latest}！`;
    status.className = 'test-result ok';
    sfx.play('star');
  } else {
    status.textContent = '已是最新版本';
    status.className = 'test-result ok';
  }
  renderChangelog(releases, log);
}

// renderChangelog 更新日志列表（全部用 textContent 填充，防 XSS）。
function renderChangelog(releases, el) {
  el.innerHTML = '';
  if (!releases.length) {
    const empty = document.createElement('p');
    empty.className = 'changelog-empty';
    empty.textContent = '暂无更新日志';
    el.appendChild(empty);
    return;
  }
  for (const r of releases) {
    const card = document.createElement('div');
    card.className = 'changelog-item';
    const head = document.createElement('div');
    head.className = 'changelog-head';
    const ver = document.createElement('span');
    ver.className = 'changelog-ver';
    ver.textContent = `v${r.version || ''}`;
    const date = document.createElement('span');
    date.className = 'changelog-date';
    date.textContent = r.pub_date || '';
    head.append(ver, date);
    if (r.title) {
      const t = document.createElement('div');
      t.className = 'changelog-title';
      t.textContent = r.title;
      head.appendChild(t);
    }
    const dl = safeURL(r.download_url);
    if (dl) {
      const a = document.createElement('a');
      a.className = 'changelog-dl';
      a.href = dl;
      a.target = '_blank';
      a.rel = 'noopener';
      a.textContent = '下载';
      head.appendChild(a);
    }
    const body = document.createElement('pre');
    body.className = 'changelog-body';
    body.textContent = r.content || '';
    card.append(head, body);
    el.appendChild(card);
  }
}

async function submitFeedback() {
  const out = $('fb-result');
  const title = $('fb-title').value.trim();
  if (!title) {
    out.textContent = '请填写标题';
    out.className = 'test-result bad';
    sfx.play('illegal');
    return;
  }
  out.textContent = '提交中…';
  out.className = 'test-result';
  const clientInfo =
    `UA: ${navigator.userAgent}\n屏幕: ${screen.width}x${screen.height} dpr=${devicePixelRatio}\n语言: ${navigator.language}\n页面: ${location.href}`;
  try {
    const resp = await fetch(BASE + 'api/feedback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        category: $('fb-category').value,
        title,
        description: $('fb-desc').value.trim(),
        contact: $('fb-contact').value.trim(),
        includeLogs: $('fb-logs').checked,
        clientInfo,
      }),
    });
    const data = await resp.json();
    if (data.ok) {
      out.textContent = `✓ ${data.message || '反馈已提交，感谢！'}`;
      out.className = 'test-result ok';
      sfx.play('star');
      $('fb-title').value = '';
      $('fb-desc').value = '';
    } else {
      out.textContent = `✗ ${data.message || '提交失败'}`;
      out.className = 'test-result bad';
      sfx.play('illegal');
    }
  } catch (e) {
    out.textContent = `✗ 网络错误：${e.message}`;
    out.className = 'test-result bad';
  }
}
