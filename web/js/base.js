// 运行时基地址：根据当前页面路径推导，使前后端在飞牛 fnOS 网关前缀与本地直连下都能工作。
//
// 本地直连： http://host/                 → BASE = "/"
// 网关部署： http://host/app/panda-xiangqi/ → BASE = "/app/panda-xiangqi/"
//
// 所有接口与 WebSocket 地址都基于 BASE 拼接，避免把网关前缀写死。
const raw = location.pathname;
const lastSlash = raw.lastIndexOf('/');
const lastSeg = raw.slice(lastSlash + 1);
// 末段含点号视为文件（如 index.html），截掉以得到目录前缀。
const dir = lastSeg && lastSeg.includes('.') ? raw.slice(0, lastSlash + 1) : raw;
export const BASE = dir.endsWith('/') ? dir : dir + '/';
