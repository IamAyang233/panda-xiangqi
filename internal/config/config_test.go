package config

import (
	"os"
	"path/filepath"
	"testing"
)

// clearEnv 把本包认的所有环境变量置空。
//
// Load 用 `if v := os.Getenv(k); v != ""` 判断，所以置空等价于「未设置」。
// 必须显式清而不是只设关心的那个：开发机上往往真的导了 QIJING_PORT 之类，
// 不清就会让断言随环境变化而随机失败。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"QIJING_PORT", "QIJING_ENGINE", "QIJING_NNUE", "QIJING_PUZZLES",
		"QIJING_OPEN_BROWSER", "QIJING_UPDATE_API", "QIJING_FEEDBACK_TOKEN",
		"QIJING_SOCKET_PATH", "QIJING_GATEWAY_PREFIX",
	} {
		t.Setenv(k, "")
	}
}

// writeConfig 在临时目录写一份 config.yaml，返回路径。
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return p
}

// TestLoadMissingFileKeepsDefaults 缺文件时必须完全退到默认值，而不是零值。
//
// 这条是启动路径上的保命行为：配置缺失或被误删时应用仍要能起来。
func TestLoadMissingFileKeepsDefaults(t *testing.T) {
	clearEnv(t)
	got := Load(filepath.Join(t.TempDir(), "不存在.yaml"))
	want := Default()
	if got != want {
		t.Fatalf("缺文件时应返回默认配置\n got=%+v\nwant=%+v", got, want)
	}
}

// TestLoadFileValues 覆盖文件解析的各条规则。
func TestLoadFileValues(t *testing.T) {
	clearEnv(t)
	p := writeConfig(t, `
# 整行注释
port: 9001

engine: /opt/pikafish
nnue_path: "/opt/engine.nnue.flat"
puzzles-dir: '/data/canju'
open_browser: yes
update_api: https://example.invalid
feedback_token: "tok-123"
gateway_prefix: /app/panda-xiangqi/

这一行没有冒号，必须被忽略
`)

	c := Load(p)
	if c.Port != 9001 {
		t.Errorf("Port = %d，期望 9001", c.Port)
	}
	if c.EnginePath != "/opt/pikafish" {
		t.Errorf("EnginePath = %q，期望 /opt/pikafish", c.EnginePath)
	}
	// 双引号必须被剥掉，否则拼路径时会带上引号。
	if c.NNUEPath != "/opt/engine.nnue.flat" {
		t.Errorf("NNUEPath = %q，期望 /opt/engine.nnue.flat（引号应被剥掉）", c.NNUEPath)
	}
	// 别名 puzzles-dir 与单引号。
	if c.PuzzlesDir != "/data/canju" {
		t.Errorf("PuzzlesDir = %q，期望 /data/canju（puzzles-dir 别名 + 单引号）", c.PuzzlesDir)
	}
	if !c.OpenBrowser {
		t.Error("open_browser: yes 应为 true")
	}
	if c.UpdateAPI != "https://example.invalid" {
		t.Errorf("UpdateAPI = %q", c.UpdateAPI)
	}
	if c.FeedbackToken != "tok-123" {
		t.Errorf("FeedbackToken = %q", c.FeedbackToken)
	}
	// 网关前缀会去掉尾部斜杠：路由拼接时多一个斜杠就会 404。
	if c.GatewayPrefix != "/app/panda-xiangqi" {
		t.Errorf("GatewayPrefix = %q，期望去掉尾部斜杠", c.GatewayPrefix)
	}
}

// TestOpenBrowserAcceptsCommonCasings 钉住布尔值的宽容度。
//
// yaml 里写 True / Yes / On 都很常见；首字母大写曾被静默当作 false，
// 表现为「配了自动打开浏览器却不打开」而没有任何提示。
func TestOpenBrowserAcceptsCommonCasings(t *testing.T) {
	for _, on := range []string{"true", "True", "TRUE", "1", "yes", "Yes", "on"} {
		clearEnv(t)
		p := writeConfig(t, "open_browser: "+on+"\n")
		if c := Load(p); !c.OpenBrowser {
			t.Errorf("open_browser: %q 应为 true", on)
		}
	}
	for _, off := range []string{"false", "False", "0", "no", "off"} {
		clearEnv(t)
		p := writeConfig(t, "open_browser: "+off+"\n")
		if c := Load(p); c.OpenBrowser {
			t.Errorf("open_browser: %q 应为 false", off)
		}
	}
}

// TestEnvOverridesFile 环境变量优先于配置文件（部署时靠它覆盖端口等）。
func TestEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	p := writeConfig(t, "port: 9001\nengine: /from/file\nsocket_path: /from/file.sock\n")
	t.Setenv("QIJING_PORT", "9002")
	t.Setenv("QIJING_SOCKET_PATH", "/run/qijing.sock")

	c := Load(p)
	if c.Port != 9002 {
		t.Errorf("Port = %d，期望 9002（环境变量赢）", c.Port)
	}
	// 环境变量只覆盖它自己那一项，其余仍来自文件。
	if c.EnginePath != "/from/file" {
		t.Errorf("EnginePath = %q，期望仍来自文件", c.EnginePath)
	}
	if c.SocketPath != "/run/qijing.sock" {
		t.Errorf("SocketPath = %q，期望来自环境变量", c.SocketPath)
	}
}

// TestSocketPathDecidesListenMode SocketPath 非空即走 Unix Socket（fnOS 网关）。
// 它是「是否暴露 TCP 端口」的唯一开关，值本身必须原样保留。
func TestSocketPathDecidesListenMode(t *testing.T) {
	clearEnv(t)
	if c := Load(filepath.Join(t.TempDir(), "无.yaml")); c.SocketPath != "" {
		t.Fatalf("未配置时 SocketPath 应为空（走本地 TCP），实为 %q", c.SocketPath)
	}
	clearEnv(t)
	t.Setenv("QIJING_SOCKET_PATH", "/var/run/app/panda-xiangqi.sock")
	if c := Load(""); c.SocketPath != "/var/run/app/panda-xiangqi.sock" {
		t.Fatalf("SocketPath = %q，期望原样保留", c.SocketPath)
	}
}

// TestPortFallback 非法端口必须退到默认值而不是带着垃圾值去监听。
func TestPortFallback(t *testing.T) {
	cases := []struct {
		name, val string
		want      int
	}{
		{"非数字", "abc", 8080},
		{"零", "0", 8080},
		{"负数", "-1", 8080},
		{"超出 65535", "70000", 8080},
		{"合法边界", "65535", 65535},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("QIJING_PORT", tc.val)
			if c := Load(""); c.Port != tc.want {
				t.Errorf("QIJING_PORT=%q ⇒ Port=%d，期望 %d", tc.val, c.Port, tc.want)
			}
		})
	}
}
