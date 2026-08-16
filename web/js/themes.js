// 主题：三套棋子皮肤（木/玉/瓷）+ 两套棋盘。

export const pieceSkins = {
  wood: {
    name: '木质',
    red: { face1: '#f7e7c8', face2: '#e2c390', rim: '#a9793f', ring: '#8a5a28', text: '#c93a20' },
    black: { face1: '#f2e4c8', face2: '#d6bd8e', rim: '#7c6236', ring: '#5d4826', text: '#2f2318' },
    shadow: 'rgba(60,35,10,0.45)',
  },
  jade: {
    name: '玉石',
    red: { face1: '#eaf7ee', face2: '#b8e0c6', rim: '#6faf8a', ring: '#3f8a63', text: '#1f7a4d' },
    black: { face1: '#f0f7f2', face2: '#c2d8cc', rim: '#5f8a74', ring: '#375c4a', text: '#173026' },
    shadow: 'rgba(20,60,40,0.4)',
  },
  porcelain: {
    name: '白瓷',
    red: { face1: '#ffffff', face2: '#e8ecf2', rim: '#b8c2d0', ring: '#8a94a8', text: '#c0392b' },
    black: { face1: '#ffffff', face2: '#e4e8ef', rim: '#a8b0c0', ring: '#767e92', text: '#232833' },
    shadow: 'rgba(30,40,60,0.4)',
  },
};

export const boardSkins = {
  maple: {
    name: '原木暖黄',
    base1: '#e8c58a', base2: '#d8ae70',
    border: '#9c6b32', line: '#5d3c14',
    text: '#5d3c14',
    coord: '#8a5a28',
    glow: 'rgba(212,169,65,0.5)',
  },
  ebony: {
    name: '黑檀深沉',
    base1: '#4a3b2c', base2: '#3a2d20',
    border: '#241a10', line: '#c9a86a',
    text: '#d8b878',
    coord: '#a98c58',
    glow: 'rgba(212,169,65,0.6)',
  },
};

export const pieceChars = {
  red: ['帅', '仕', '相', '马', '车', '炮', '兵'],
  black: ['将', '士', '象', '马', '车', '炮', '卒'],
};
