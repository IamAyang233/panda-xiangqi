// Package qiweb 内嵌前端静态资源（web/ 目录，无构建步骤，浏览器直接加载 ES 模块）。
// 产品名：熊猫象棋（Panda Xiangqi）。
package qiweb

import "embed"

// WebFS 前端资源根（内容在 web/ 下）。
//
//go:embed all:web
var WebFS embed.FS
