// mockllm OpenAI 兼容模拟服务（计划书 §10 LLM 测试基建）：
// 从提示词中的"可选合法着法"表抽取一手返回，用于大模型对战的端到端联调。
//
// 用法：go run ./cmd/mockllm [-addr :9099] [-mode legal|illegal|timeout]
//   legal   —— 返回合法着法 JSON + 棋评（默认）
//   illegal —— 永远返回非法内容，触发重试→本地引擎降级链
//   timeout —— 响应前长睡，触发超时降级
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var legalRe = regexp.MustCompile(`(?:可选合法着法|引擎推荐候选着法（由强到弱）)：([a-i][0-9][a-i][0-9](?:, [a-i][0-9][a-i][0-9])*)`)

func main() {
	addr := flag.String("addr", ":9099", "监听地址")
	mode := flag.String("mode", "legal", "legal | illegal | timeout")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		content := pickMove(req.Messages, *mode)
		resp := map[string]any{
			"id":      "mockllm",
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	// 简单模型列表端点（部分客户端会探测）
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mock"}]}`))
	})

	fmt.Printf("熊猫象棋 mockllm（mode=%s）已启动: http://localhost%s/v1\n", *mode, strings.TrimSuffix(*addr, ":9099"))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}

func pickMove(msgs []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}, mode string) string {
	if mode == "timeout" {
		time.Sleep(60 * time.Second)
		return ""
	}
	var lastUser string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = msgs[i].Content
			break
		}
	}
	m := legalRe.FindStringSubmatch(lastUser)
	if mode == "illegal" || len(m) < 2 {
		return "我认为应该走出一步妙棋，车九平十！"
	}
	moves := strings.Split(m[1], ", ")
	mv := moves[len(moves)/3] // 取中部一手，风格稳定
	return fmt.Sprintf(`{"from":"%s","to":"%s","comment":"熊猫咬合一手（mock）"}`, mv[:2], mv[2:])
}
