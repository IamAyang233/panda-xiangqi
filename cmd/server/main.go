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

	qiweb "github.com/IamAyang233/panda-xiangqi"
	"github.com/IamAyang233/panda-xiangqi/internal/api"
	"github.com/IamAyang233/panda-xiangqi/internal/config"
	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/record"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
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
	// 自定义残局（自摆局面）：独立目录、独立 Store，与题库互不干扰。
	custom, customDir := mustCustom(cfg.CustomDir)
	if customDir != "" {
		// 明确打出自摆残局的落盘位置：用户反馈「存了找不到」时看日志即可定位。
		log.Printf("自定义残局目录: %s（%d 条）", customDir, custom.Count())
	}

	// 棋谱（对局记录）：同样独立目录、独立 Store。保存由用户在结算弹窗触发。
	records, recordsDir := mustRecords(cfg.RecordsDir)
	if recordsDir != "" {
		log.Printf("棋谱目录: %s（%d 局）", recordsDir, records.Count())
	}

	engines := engine.NewManagerWithNNUE(cfg.EnginePath, cfg.NNUEPath)
	defer engines.Close()
	log.Printf("引擎: %s（内嵌 Go 引擎: %v，外置 UCI 兜底: %v）",
		engines.EngineName(), engines.HasNative(), engines.HasUCI())
	// 引擎探测诊断：外置 UCI 引擎没启动起来时，日志里能看到每个候选路径的失败原因
	// （文件缺失 / 权限被拒 / 架构不符 / 握手超时 / 权重缺失），用户反馈时按图索骥。
	for _, d := range engines.Diagnostics() {
		log.Printf("引擎诊断: %s", d)
	}

	srv := &api.Server{
		Sessions:      session.NewManager(),
		Engines:       engines,
		Puzzles:       puzzles,
		Custom:        custom,
		CustomDir:     customDir,
		Records:       records,
		RecordsDir:    recordsDir,
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
		// ⚠️ 只删「确实是 socket」的路径见 cleanStaleSocket：删除不可逆，而
		// SocketPath 来自配置文件，写错成普通文件时不该把它删掉。
		if err := cleanStaleSocket(cfg.SocketPath); err != nil {
			log.Fatalf("Socket 路径 %s 不可用: %v", cfg.SocketPath, err)
		}
		ln, err = net.Listen("unix", cfg.SocketPath)
		if err != nil {
			log.Fatalf("监听 Unix Socket %s 失败: %v", cfg.SocketPath, err)
		}
		// 退出时移除 socket 文件（此处 bind 已成功，该路径必定是我们的 socket）。
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

// cleanStaleSocket 清理残留的 Unix Socket 文件，供启动时 bind 前调用。
//
// 语义：
//   - 路径不存在 → 什么都不做（正常情况）
//   - 路径是 socket → 删掉（上一个实例异常退出留下的，不删会导致 bind 失败）
//   - 路径存在但**不是** socket → 返回错误，**绝不删除**
//
// 第三条是重点：SocketPath 来自配置文件，写错成某个普通文件路径时，
// 无条件的 `os.Remove` 会把那个文件删掉，而删除是不可逆的。
// 宁可启动失败并给出明确原因，也不要静默毁掉用户的文件。
func cleanStaleSocket(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil // 不存在（或无法 stat）→ 交给 net.Listen 报错
	}
	if st.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("已存在且不是 socket（配置写错了？拒绝覆盖）")
	}
	return os.Remove(path)
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

// mustCustom 载入自定义残局目录。目录建不出来或不可读时**降级为空库**：
// 自摆残局是附加功能，不该因为它而让整个服务起不来；此时保存只活在内存里
// （进程重启即丢），这一事实由 /api/status 的 customWritable 如实暴露。
// 返回的 dir 为 "" 即表示内存态。
func mustCustom(dir string) (*puzzle.Store, string) {
	if dir == "" {
		return puzzle.NewEmpty(), ""
	}
	// 目录里的坏文件由 NewStore 逐条跳过并告警，不会整体失败。
	st, err := puzzle.LoadDir(dir)
	if err == nil {
		return st, dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("自定义残局目录 %s 不可用，本次仅内存保存: %v", dir, err)
		return puzzle.NewEmpty(), ""
	}
	if st, err = puzzle.LoadDir(dir); err != nil {
		log.Printf("自定义残局目录 %s 载入失败，本次仅内存保存: %v", dir, err)
		return puzzle.NewEmpty(), ""
	}
	return st, dir
}

// mustRecords 载入棋谱目录，语义与 mustCustom 一致：目录不可用时降级为空库 +
// 内存态，不让这一个附加功能把整个服务拖住；降级事实由 /api/status 的
// recordsWritable 如实暴露。返回的 dir 为 "" 即表示内存态。
func mustRecords(dir string) (*record.Store, string) {
	if dir == "" {
		return record.NewEmpty(), ""
	}
	st, err := record.LoadDir(dir)
	if err == nil {
		return st, dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("棋谱目录 %s 不可用，本次仅内存保存: %v", dir, err)
		return record.NewEmpty(), ""
	}
	if st, err = record.LoadDir(dir); err != nil {
		log.Printf("棋谱目录 %s 载入失败，本次仅内存保存: %v", dir, err)
		return record.NewEmpty(), ""
	}
	return st, dir
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
