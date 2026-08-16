// Package config 服务端配置：config.yaml（扁平 key: value 子集）+ 环境变量覆盖。
package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Config 服务配置。
type Config struct {
	Port          int    // 监听端口（本地开发 TCP 模式）
	EnginePath    string // 皮卡鱼路径（空 = 自动探测）
	PuzzlesDir    string // 外置残局目录（空 = 使用内嵌）
	OpenBrowser   bool   // 启动时自动打开浏览器（仅本地 TCP 模式有效）
	UpdateAPI     string // PanDa 推送更新服务入口（默认公网域名）
	FeedbackToken string // 反馈提交共享 Token

	// 飞牛 fnOS 统一网关部署相关（由 cmd/main 注入，本地开发为空）。
	SocketPath    string // 监听的 Unix Socket 路径；非空时优先于 Port 以 Socket 模式运行（无需 root）
	GatewayPrefix string // 网关前缀，例如 /app/panda-xiangqi；非空时所有路由挂载到该前缀下
}

// Default 默认配置。
func Default() Config {
	return Config{
		Port:          8080,
		OpenBrowser:   true,
		UpdateAPI:     "https://www.aykeji.cn",
		FeedbackToken: "fnos-panda-xiangqi-feedback",
	}
}

// Load 依次应用：默认值 → configPath（若存在）→ 环境变量。
// 环境变量：QIJING_PORT / QIJING_ENGINE / QIJING_PUZZLES / QIJING_OPEN_BROWSER /
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
	if v := os.Getenv("QIJING_PUZZLES"); v != "" {
		apply(&c, "puzzles", v)
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
	case "puzzles", "puzzles_dir", "puzzles-dir":
		c.PuzzlesDir = val
	case "open_browser", "open-browser", "openbrowser":
		c.OpenBrowser = val == "true" || val == "1" || val == "yes"
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
