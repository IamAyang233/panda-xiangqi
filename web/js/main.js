// 熊猫象棋前端入口。
import { showScreen, currentScreen, toast, confirmDialog } from './ui.js';
import { initLobby } from './screens/lobby.js';
import { initPuzzles, refresh as refreshPuzzles, siblingOf, selectDiff } from './screens/puzzles.js';
import { initSetup } from './screens/setup.js';
import { initRecords } from './screens/records.js';
import { initSettings } from './screens/settings.js';
import { initAbout } from './screens/about.js';
import { GameScreen } from './screens/game.js';
import { store } from './store.js';
import { sfx } from './audio.js';

const game = new GameScreen();

initLobby(async (mode, opts) => {
  await game.start(mode, opts);
});

initPuzzles(async (mode, opts) => {
  await game.start(mode, opts);
});

// 摆局屏：摆好并保存后直接进入挑战（保存由 setup 屏自己做，这里只负责开局）
initSetup(async (mode, opts) => {
  await game.start(mode, opts);
});

// 残局对局内逐关切换（上一关/下一关），范围跟随残局列表当前筛选
game.onPuzzleStep = async (delta) => {
  const sib = siblingOf(game.puzzleId, delta);
  if (!sib) {
    toast(delta < 0 ? '已经是该级别的第一关' : '已经是该级别的最后一关', false, 2000);
    return;
  }
  if (game.conn && !game.gameOver && game.moves.length) {
    const ok = await confirmDialog('当前残局尚未完成，确定切换到相邻关卡？', { okText: '切换' });
    if (!ok) return;
  }
  await game.start('puzzle', { puzzleId: sib.id });
};

// 退出到残局列表时刷新星级/进度；自摆残局会带上 '自定义'，先把页签切过去再渲染
game.onExitToPuzzles = (diff) => {
  if (diff) selectDiff(diff);
  refreshPuzzles();
};

initSettings(() => {
  game.renderer.setTheme({
    skin: store.theme.pieces,
    boardSkin: store.theme.board,
  });
  game.renderer.particles.enabled = store.theme.particles;
});

initRecords();

initAbout();

// 顶栏返回：逐层返回（残局对局 → 残局列表 → 大厅；其他模式 → 大厅）
document.getElementById('btn-home').onclick = async () => {
  sfx.play('button');
  const inGame = currentScreen() === 'game';
  const back = inGame && game.mode === 'puzzle' ? '残局列表' : '大厅';
  if (inGame && game.conn && !game.gameOver && game.moves.length) {
    const ok = await confirmDialog(`对局进行中，确定返回${back}？`, { okText: `返回${back}` });
    if (!ok) {
      // 取消返回：回到进行中的对局画面
      showScreen('game');
      return;
    }
  }
  if (inGame) { game.exit(); return; }   // exit() 内部按模式决定去向
  showScreen('lobby');
};

// 首次交互解锁音频上下文
window.addEventListener('pointerdown', () => sfx.play('button'), { once: true });

// 启动时优先尝试恢复「进行中」的对局（断网/刷新后凭存档 gameId 重连）；
// 无存档或恢复失败则落入大厅。
const restored = await game.restore();
if (!restored) {
  showScreen('lobby');
  toast('欢迎来到熊猫象棋 · 选择一种模式开始对弈', false, 3200);
}
