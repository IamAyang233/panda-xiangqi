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

const typeToFen = { 1: 'K', 2: 'A', 3: 'B', 4: 'N', 5: 'R', 6: 'C', 7: 'P' };

// boardToFEN -> FEN 字符串。与 parseFEN 的**往返**关系是：board→FEN→board 恒等；
// 反向不恒等 —— parseFEN 也接受 E/H（象/马的另一种写法），而这里固定输出 B/N，
// 所以 FEN→board→FEN 会把 E/H 规范化为 B/N。服务端两种写法都认，不影响对局。
// board 是 'f,r' -> {color,type}，turn 为 'red'|'black'；红方大写、黑方小写。
// 空行用数字压缩（9 表示整行全空），行序按 r 从 9 到 0（与 parseFEN 的 9-i 对应）。
export function boardToFEN(board, turn = 'red') {
  const rows = [];
  for (let r = RANKS - 1; r >= 0; r--) {
    let row = '';
    let empty = 0;
    for (let f = 0; f < FILES; f++) {
      const p = board.get(`${f},${r}`);
      if (!p) { empty++; continue; }
      if (empty) { row += empty; empty = 0; }
      const ch = typeToFen[p.type] || '';
      row += p.color === 'red' ? ch : ch.toLowerCase();
    }
    if (empty) row += empty;
    rows.push(row || String(FILES));
  }
  return `${rows.join('/')} ${turn === 'black' ? 'b' : 'w'}`;
}
