package engine

import (
	"strings"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestDiagnosticsReportsSIMD 诊断输出必须带上累加器内核路径。
//
// 低配设备上排查「引擎起不来 / 跑得异常慢」时，第一件要确认的就是它走的
// 是 AVX2 还是标量兜底。这条信息不该只存在于代码里 —— 用户要能自查。
func TestDiagnosticsReportsSIMD(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()
	if err := e.Warmup(); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	lines := e.Diagnostics()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, nnue.SIMDStatus()) {
		t.Errorf("诊断输出未包含累加器内核状态（期望包含 %q）：%v", nnue.SIMDStatus(), lines)
	}
	t.Logf("诊断输出：%v", lines)
}
