// 熊猫象棋（Panda Xiangqi）服务入口：单二进制，内嵌前端与残局，零依赖启动。
package main

import (
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/IamAyang233/panda-xiangqi/internal/api"
	"github.com/IamAyang233/panda-xiangqi/internal/config"
	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
	qiweb "github.com/IamAyang233/panda-xiangqi"
)

func main() {
	// 单实例互斥：已在跑则只开浏览器并退出，防止端口残留。
	if !acquireSingleInstance() {
		if cfg0 := config.Load(findConfigFile()); cfg0.Port > 0 {
			go openBrowser(fmt.Sprintf("http://localhost:%d", cfg0.Port))
		}
		log.Println("检测到已在运行，仅打开浏览器后退出。")
		return
	}
	defer releaseSingleInstance()
	cfg := config.Load(findConfigFile())

	// 版本号单一来源 = 随包 manifest；启动时读取一次，避免与 manifest 脱节。
	api.InitAppVersion()
	log.Printf("熊猫象棋版本: %s", api.AppVersion)

	// 静态资源：优先磁盘 web/dist（未来 Vite 产物），否则磁盘 web/（开发），否则内嵌
	var static fs.FS
	if st, err := os.Stat("web/dist"); err == nil && st.IsDir() {
		static = os.DirFS("web/dist")
	} else if st, err := os.Stat("web"); err == nil && st.IsDir() {
		static = os.DirFS("web")
	} else {
		sub, err := fs.Sub(qiweb.WebFS, "web")
		if err != nil {
			log.Fatalf("内嵌静态资源缺失: %v", err)
		}
		static = sub
	}

	// 残局：外置目录优先，否则内嵌
	puzzles := mustPuzzles(cfg.PuzzlesDir)

	engines := engine.NewManager(cfg.EnginePath)
	defer engines.Close()
	log.Printf("引擎: %s（皮卡鱼可用: %v）", engines.EngineName(), engines.HasUCI())

	srv := &api.Server{
		Sessions:      session.NewManager(),
		Engines:       engines,
		Puzzles:       puzzles,
		Static:        static,
		UpdateAPI:     cfg.UpdateAPI,
		FeedbackToken: cfg.FeedbackToken,
		GatewayPrefix: cfg.GatewayPrefix,
	}

	httpSrv := &http.Server{Handler: srv.Handler()}

	// 监听模式：优先 Unix Socket（飞牛 fnOS 统一网关，无需 root），否则本地 TCP。
	var ln net.Listener
	var err error
	var url string
	if cfg.SocketPath != "" {
		// 清理残留 socket 文件，避免 bind 失败。
		if _, statErr := os.Stat(cfg.SocketPath); statErr == nil {
			_ = os.Remove(cfg.SocketPath)
		}
		ln, err = net.Listen("unix", cfg.SocketPath)
		if err != nil {
			log.Fatalf("监听 Unix Socket %s 失败: %v", cfg.SocketPath, err)
		}
		// 退出时移除 socket 文件。
		defer os.Remove(cfg.SocketPath)
		mode := "本地 TCP"
		if cfg.GatewayPrefix != "" {
			mode = "飞牛 fnOS 统一网关 (" + cfg.GatewayPrefix + ")"
		}
		log.Printf("熊猫象棋已启动（%s），Socket: %s（残局 %d 关）", mode, cfg.SocketPath, puzzles.Count())
	} else {
		// 端口被占用时自动顺延，避免「一启动就崩」。最多尝试 21 个端口（cfg.Port ~ +20）。
		var tried []string
		for off := 0; off <= 20; off++ {
			port := cfg.Port + off
			if port > 65535 {
				break
			}
			addr := fmt.Sprintf(":%d", port)
			l, e := net.Listen("tcp", addr)
			if e == nil {
				ln = l
				break
			}
			tried = append(tried, addr)
		}
		if ln == nil {
			log.Fatalf("监听端口失败（已尝试 %v）：请改用其他端口，例如设置环境变量 QIJING_PORT=9090", tried)
		}
		cfg.Port = ln.Addr().(*net.TCPAddr).Port
		url = fmt.Sprintf("http://localhost:%d", cfg.Port)
		if len(tried) > 0 {
			log.Printf("端口 %v 被占用，已改用 %d", tried, cfg.Port)
		}
		log.Printf("熊猫象棋已启动: %s （残局 %d 关）", url, puzzles.Count())
	}

	if cfg.OpenBrowser && url != "" {
		go openBrowser(url)
	}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务异常: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	log.Println("正在退出…")
	_ = httpSrv.Close()
}

func findConfigFile() string {
	for _, p := range []string{"config.yaml", "config.yml"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func mustPuzzles(dir string) *puzzle.Store {
	if dir != "" {
		if st, err := puzzle.LoadDir(dir); err == nil {
			return st
		}
		log.Printf("外置残局目录 %s 加载失败，改用内嵌", dir)
	}
	st, err := puzzle.Embedded()
	if err != nil {
		log.Fatalf("内嵌残局加载失败: %v", err)
	}
	return st
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("自动打开浏览器失败（请手动访问 %s）: %v", url, err)
	}
}
