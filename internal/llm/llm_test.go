package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// mockServer 模拟 OpenAI 兼容服务：按顺序返回预设回复。
func mockServer(t *testing.T, replies []string, delay time.Duration) *httptest.Server {
	t.Helper()
	i := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if i >= len(replies) {
			i = len(replies) - 1
		}
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": replies[i]}}},
		}
		i++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func testConfig(url string) Config {
	c := DefaultConfig()
	c.BaseURL = url
	c.Model = "mock"
	c.TimeoutMs = 2000
	return c
}

func TestParseValidJSON(t *testing.T) {
	p := game.NewPosition()
	legal := p.LegalMoves(game.Red)
	mv, comment, ok := parseMove(`{"from":"h2","to":"e2","comment":"中炮开局"}`, p, legal)
	if !ok || mv.String() != "h2e2" || comment != "中炮开局" {
		t.Errorf("合法 JSON 解析失败: ok=%v mv=%v comment=%q", ok, mv, comment)
	}
}

func TestParseLooseJSON(t *testing.T) {
	p := game.NewPosition()
	legal := p.LegalMoves(game.Red)
	mv, _, ok := parseMove("我的想法是 {\"from\":\"b2\",\"to\":\"e2\",\"comment\":\"还中炮\"} 请考虑", p, legal)
	if !ok || mv.String() != "b2e2" {
		t.Errorf("宽松 JSON 提取失败: ok=%v mv=%v", ok, mv)
	}
}

func TestParseChineseNotation(t *testing.T) {
	p := game.NewPosition()
	legal := p.LegalMoves(game.Red)
	mv, _, ok := parseMove("我走 炮二平五！", p, legal)
	if !ok || mv.String() != "h2e2" {
		t.Errorf("中文着法匹配失败: ok=%v mv=%v", ok, mv)
	}
}

func TestParseUCIString(t *testing.T) {
	p := game.NewPosition()
	legal := p.LegalMoves(game.Red)
	mv, _, ok := parseMove("h2e2", p, legal)
	if !ok || mv.String() != "h2e2" {
		t.Errorf("UCI 串解析失败: ok=%v mv=%v", ok, mv)
	}
}

// 真实模型常见输出：{"best_move": "b7e7"}（非约定 from/to 格式）。
func TestParseBestMoveField(t *testing.T) {
	p := game.NewPosition()
	p.Make(mustMove(t, "h2e2")) // 炮二平五后轮黑
	legal := p.LegalMoves(game.Black)
	mv, _, ok := parseMove(`{"best_move": "b7e7"}`, p, legal)
	if !ok || mv.String() != "b7e7" {
		t.Errorf("best_move 字段解析失败: ok=%v mv=%v", ok, mv)
	}
	// thought 字段兼作棋评
	mv2, c, ok2 := parseMove(`{"bestmove":"h9g7","thought":"出马护中卒"}`, p, legal)
	if !ok2 || mv2.String() != "h9g7" || c != "出马护中卒" {
		t.Errorf("bestmove+thought 解析失败: ok=%v mv=%v c=%q", ok2, mv2, c)
	}
}

func mustMove(t *testing.T, uci string) game.Move {
	t.Helper()
	m, ok := game.MoveFromUCI(uci)
	if !ok {
		t.Fatalf("非法 UCI %s", uci)
	}
	return m
}

func TestRetryThenSuccess(t *testing.T) {
	// 第 1 次非法，第 2 次合法 → 成功且 Attempts=2
	srv := mockServer(t, []string{
		`{"from":"z9","to":"z9","comment":"乱走"}`,
		`{"from":"h2","to":"e2","comment":"中炮"}`,
	}, 0)
	defer srv.Close()
	p := game.NewPosition()
	player := NewPlayer(testConfig(srv.URL))
	res, err := player.BestMove(context.Background(), p, nil)
	if err != nil || res.Fallback {
		t.Fatalf("重试后应成功: err=%v fallback=%v", err, res.Fallback)
	}
	if res.Move.String() != "h2e2" || res.Attempts != 2 || res.Comment != "中炮" {
		t.Errorf("结果异常: %+v", res)
	}
}

func TestFallbackAfterThreeFailures(t *testing.T) {
	srv := mockServer(t, []string{
		`{"from":"x","to":"y"}`, `{"from":"x","to":"y"}`, `{"from":"x","to":"y"}`,
	}, 0)
	defer srv.Close()
	p := game.NewPosition()
	player := NewPlayer(testConfig(srv.URL))
	res, _ := player.BestMove(context.Background(), p, nil)
	if !res.Fallback {
		t.Error("三次失败后应降级本地引擎")
	}
	if !p.IsLegal(res.Move) {
		t.Errorf("降级着法应合法: %v", res.Move)
	}
}

// 引擎候选模式：解析失败时降级直接取候选首位（零搜索延迟）；成功路径模型选中候选之一。
func TestEngineAssistMode(t *testing.T) {
	srv := mockServer(t, []string{`{"best_move": "zz9z9"}`}, 0)
	defer srv.Close()
	p := game.NewPosition()
	player := NewPlayer(testConfig(srv.URL))

	eng := engine.NewSimpleEngine()
	cands := eng.RankedMoves(context.Background(), p, 4, 3)
	if len(cands) != 3 {
		t.Fatalf("候选数异常: %d", len(cands))
	}
	res, _ := player.BestMove(context.Background(), p, cands)
	if !res.Fallback || res.Move != cands[0] {
		t.Errorf("候选模式降级应取首位候选: got %v want %v", res.Move, cands[0])
	}

	srv2 := mockServer(t, []string{`{"best_move": "` + cands[2].String() + `", "comment": "稳健"}`}, 0)
	defer srv2.Close()
	player2 := NewPlayer(testConfig(srv2.URL))
	res2, err := player2.BestMove(context.Background(), p, cands)
	if err != nil || res2.Fallback || res2.Move != cands[2] || res2.Comment != "稳健" {
		t.Errorf("候选模式成功路径异常: %+v err=%v", res2, err)
	}
}

func TestFallbackOnTimeout(t *testing.T) {
	srv := mockServer(t, []string{`{"from":"h2","to":"e2"}`}, 500*time.Millisecond)
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg.TimeoutMs = 100 // 立即超时
	p := game.NewPosition()
	player := NewPlayer(cfg)
	res, _ := player.BestMove(context.Background(), p, nil)
	if !res.Fallback {
		t.Error("超时应降级本地引擎")
	}
}

func TestFallbackOnBadConfig(t *testing.T) {
	p := game.NewPosition()
	player := NewPlayer(Config{BaseURL: "", Model: ""})
	res, _ := player.BestMove(context.Background(), p, nil)
	if !res.Fallback || !p.IsLegal(res.Move) {
		t.Errorf("配置缺失应降级且合法: %+v", res)
	}
}

func TestPing(t *testing.T) {
	srv := mockServer(t, []string{"pong"}, 0)
	defer srv.Close()
	c := NewClient(testConfig(srv.URL))
	if _, err := c.Ping(context.Background()); err != nil {
		t.Errorf("Ping 失败: %v", err)
	}
}

func TestPromptIncludesLegalMoves(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"from\":\"h2\",\"to\":\"e2\"}"}}]}`))
	}))
	defer srv.Close()
	p := game.NewPosition()
	player := NewPlayer(testConfig(srv.URL))
	if _, err := player.BestMove(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, "h2e2") {
		t.Error("提示词应包含合法着法表")
	}
	if !strings.Contains(gotBody, "rnbakabnr") {
		t.Error("提示词应包含 FEN")
	}
}
