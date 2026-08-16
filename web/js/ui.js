// 屏幕路由与通用 UI 工具。
export function showScreen(name) {
  for (const s of ['lobby', 'game', 'puzzles', 'about']) {
    document.getElementById(`screen-${s}`).hidden = s !== name;
  }
  document.getElementById('btn-home').hidden = name === 'lobby';
}

let toastTimer = null;
export function toast(msg, isError = false, ms = 2600) {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.className = 'show' + (isError ? ' error' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.className = ''; }, ms);
}

// confirmDialog 主题化确认弹窗（替代浏览器原生 confirm）。
// resolve(true) = 确认，resolve(false) = 取消/遮罩/ESC。
export function confirmDialog(text, { danger = false, okText = '确定', cancelText = '取消' } = {}) {
  return new Promise((resolve) => {
    const overlay = document.createElement('div');
    overlay.className = 'overlay confirm-overlay';
    overlay.innerHTML = `
      <div class="confirm-card">
        <div class="confirm-icon">${danger ? '!' : '?'}</div>
        <div class="confirm-text"></div>
        <div class="confirm-actions">
          <button class="ctl-btn confirm-cancel"></button>
          <button class="ctl-btn confirm-ok"></button>
        </div>
      </div>`;
    overlay.querySelector('.confirm-text').textContent = text;
    overlay.querySelector('.confirm-cancel').textContent = cancelText;
    const okBtn = overlay.querySelector('.confirm-ok');
    okBtn.textContent = okText;
    if (danger) okBtn.classList.add('danger-strong');
    document.body.appendChild(overlay);

    const done = (val) => {
      overlay.remove();
      document.removeEventListener('keydown', onKey);
      resolve(val);
    };
    const onKey = (e) => { if (e.key === 'Escape') done(false); };
    document.addEventListener('keydown', onKey);
    overlay.addEventListener('pointerdown', (e) => { if (e.target === overlay) done(false); });
    overlay.querySelector('.confirm-cancel').onclick = () => done(false);
    okBtn.onclick = () => done(true);
    okBtn.focus();
  });
}
