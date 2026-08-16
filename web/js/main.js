// 熊猫象棋前端入口。
import { showScreen, toast, confirmDialog } from './ui.js';
import { initLobby } from './screens/lobby.js';
import { initPuzzles, refresh as refreshPuzzles } from './screens/puzzles.js';
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

initSettings(() => {
  game.renderer.setTheme({
    skin: store.theme.pieces,
    boardSkin: store.theme.board,
  });
  game.renderer.particles.enabled = store.theme.particles;
});

initAbout();

document.getElementById('btn-home').onclick = async () => {
  sfx.play('button');
  if (game.conn && !game.gameOver && game.moves.length) {
    const ok = await confirmDialog('对局进行中，确定返回大厅？', { okText: '返回大厅' });
    if (!ok) {
      // 取消返回：回到进行中的对局画面
      showScreen('game');
      return;
    }
  }
  game.exit();
  refreshPuzzles();
};

// 首次交互解锁音频上下文
window.addEventListener('pointerdown', () => sfx.play('button'), { once: true });

showScreen('lobby');
toast('欢迎来到熊猫象棋 · 选择一种模式开始对弈', false, 3200);
