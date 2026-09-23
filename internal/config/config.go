// Package config 服务端配置：config.yaml（扁平 key: value 子集）+ 环境变量覆盖。
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 服务配置。
type Config struct {
	Port          int    // 监听端口（本地开发 TCP 模式）
	EnginePath    string // 皮卡鱼路径（空 = 自动探测）
	NNUEPath      string // 内嵌 Go 引擎的 NNUE 权重（.flat）路径；空 = 自动探测
	PuzzlesDir    string // 外置残局目录（空 = 使用内嵌）
	CustomDir     string // 自定义残局目录（自摆局面落盘处，全服共享；空 = 用 ./custom-puzzles）
	OpenBrowser   bool   // 启动时自动打开浏览器（仅本地 TCP 模式有效）
	UpdateAPI     string // PanDa 推送更新服务入口（默认公网域名）
	FeedbackToken string // 反馈提交共享 Token

	// 飞牛 fnOS 统一网关部署相关（由 cmd/main 注入，本地开发为空）。
	SocketPath    string // 监听的 Unix Socket 路径；非空时优先于 Port 以 Socket 模式运行（无需 root）
	GatewayPrefix string // 网关前缀，例如 /app/panda-xiangqi；非空时所有路由挂载到该前缀下

	// customSet 记录「自定义残局目录」是否被显式配置过（config.yaml 或 QIJING_CUSTOM）。
	// 未显式配置时才启用下面的 fnOS 目录兜底，避免覆盖用户的明确选择。
	customSet bool
}

// Default 默认配置。
func Default() Config {
	return Config{
		Port:          8080,
		OpenBrowser:   true,
		UpdateAPI:     "https://www.aykeji.cn",
		FeedbackToken: "fnos-panda-xiangqi-feedback",
		CustomDir:     DefaultCustomDir,
	}
}

// DefaultCustomDir 自定义残局目录的默认名（相对工作目录）。
const DefaultCustomDir = "custom-puzzles"

// Load 依次应用：默认值 → configPath（若存在）→ 环境变量。
// 环境变量：QIJING_PORT / QIJING_ENGINE / QIJING_PUZZLES / QIJING_CUSTOM / QIJING_OPEN_BROWSER /
// QIJING_UPDATE_API / QIJING_FEEDBACK_TOKEN / QIJING_SOCKET_PATH / QIJING_GATEWAY_PREFIX。
func Load(configPath string) Config {
	c := Default()
	if f, err := os.Open(configPath); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			apply(&c, strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	if v := os.Getenv("QIJING_PORT"); v != "" {
		apply(&c, "port", v)
	}
	if v := os.Getenv("QIJING_ENGINE"); v != "" {
		apply(&c, "engine", v)
	}
	if v := os.Getenv("QIJING_NNUE"); v != "" {
		apply(&c, "nnue", v)
	}
	if v := os.Getenv("QIJING_PUZZLES"); v != "" {
		apply(&c, "puzzles", v)
	}
	if v := os.Getenv("QIJING_CUSTOM"); v != "" {
		apply(&c, "custom", v)
	}
	if v := os.Getenv("QIJING_OPEN_BROWSER"); v != "" {
		apply(&c, "open_browser", v)
	}
	if v := os.Getenv("QIJING_UPDATE_API"); v != "" {
		apply(&c, "update_api", v)
	}
	if v := os.Getenv("QIJING_FEEDBACK_TOKEN"); v != "" {
		apply(&c, "feedback_token", v)
	}
	if v := os.Getenv("QIJING_SOCKET_PATH"); v != "" {
		apply(&c, "socket_path", v)
	}
	if v := os.Getenv("QIJING_GATEWAY_PREFIX"); v != "" {
		apply(&c, "gateway_prefix", v)
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 8080
	}
	// 自定义残局目录：用户没显式配置时，按飞牛目录约定落到框架的运行时数据目录
	// （TRIM_PKGVAR，升级替换应用目录时不会把自摆残局一起清掉）。
	// 非 fnOS 环境保持相对路径 ./custom-puzzles —— 启动脚本会先 cd 到应用目录，
	// 因此它落在应用目录内，符合「应用数据保存在应用目录内」的约定。
	if !c.customSet {
		if v := os.Getenv("TRIM_PKGVAR"); v != "" {
			c.CustomDir = filepath.Join(v, DefaultCustomDir)
		}
	}
	return c
}

func apply(c *Config, key, val string) {
	val = strings.Trim(val, `"'`)
	switch strings.ToLower(key) {
	case "port":
		if n, err := strconv.Atoi(val); err == nil {
			c.Port = n
		}
	case "engine", "engine_path", "engine-path":
		c.EnginePath = val
	case "nnue", "nnue_path", "nnue-path", "weights":
		c.NNUEPath = val
	case "puzzles", "puzzles_dir", "puzzles-dir":
		c.PuzzlesDir = val
	case "custom", "custom_dir", "custom-dir", "custom_puzzles":
		// 自摆残局落盘目录。刻意与 puzzles 分开：puzzles 是「整体替换内嵌题库」，
		// 把自摆残局写进去会要求用户必须配该目录，否则 3576 关会消失。
		c.CustomDir = val
		c.customSet = true
	case "open_browser", "open-browser", "openbrowser":
		// ⚠️ 值也要小写化：yaml 里写 `open_browser: True` / `Yes` 很常见，
		// 原实现只比对 "true"/"1"/"yes" 三个小写串，遇到首字母大写会**静默变成 false**。
		v := strings.ToLower(val)
		c.OpenBrowser = v == "true" || v == "1" || v == "yes" || v == "on"
	case "update_api", "update-api", "updateapi":
		c.UpdateAPI = val
	case "feedback_token", "feedback-token", "feedbacktoken":
		c.FeedbackToken = val
	case "socket_path", "socket-path", "socketpath":
		c.SocketPath = val
	case "gateway_prefix", "gateway-prefix", "gatewayprefix":
		c.GatewayPrefix = strings.TrimRight(val, "/")
	}
}
