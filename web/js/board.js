// 棋盘模型：FEN 解析、坐标换算、翻转。
// 坐标系与后端一致：列 a~i（红方左侧起），行 0~9（红方底线起）。

export const FILES = 9;
export const RANKS = 10;

const fenToType = {
  K: 1, A: 2, B: 3, E: 3, N: 4, H: 4, R: 5, C: 6, P: 7,
  k: 1, a: 2, b: 3, e: 3, n: 4, h: 4, r: 5, c: 6, p: 7,
};

// parseFEN -> { board: Map<'f,r', {color,type}>, turn: 'red'|'black' }
export function parseFEN(fen) {
  const board = new Map();
  const fields = fen.trim().split(/\s+/);
  const rows = fields[0].split('/');
  for (let i = 0; i < rows.length; i++) {
    const r = 9 - i;
    let f = 0;
    for (const ch of rows[i]) {
      if (ch >= '1' && ch <= '9') { f += +ch; continue; }
      const t = fenToType[ch];
      if (t && f < 9) {
        board.set(`${f},${r}`, { color: ch === ch.toUpperCase() ? 'red' : 'black', type: t });
        f++;
      }
    }
  }
  return { board, turn: fields[1] === 'b' ? 'black' : 'red' };
}

export function sqName(f, r) { return String.fromCharCode(97 + f) + r; }
export function parseSq(name) {
  if (!/^[a-i][0-9]$/.test(name)) return null;
  return { f: name.charCodeAt(0) - 97, r: +name[1] };
}
