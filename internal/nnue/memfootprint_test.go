package nnue

import (
	"runtime"
	"testing"
)

// TestWeightMemoryFootprint 测量权重加载的内存开销。
//
// 这不是性能问题而是**设备兼容性**问题：fnOS 用户里有不少低配设备
// （内存 512MB~2GB 的小主机/NAS）。若加载权重需要两份 68MB
// （先 os.ReadFile 读成 raw，再 Parse 出结构体），峰值能到 136MB，
// 叠加置换表和运行时本身就可能触发 OOM 或 swap 抖动。
func TestWeightMemoryFootprint(t *testing.T) {
	var m runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&m)
	heapBefore, totalBefore := m.HeapAlloc, m.TotalAlloc

	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	runtime.GC()
	runtime.ReadMemStats(&m)
	resident := m.HeapAlloc - heapBefore
	// TotalAlloc 是累计分配量，能反映加载过程中一共申请过多少内存，
	// 即峰值的一个上界。
	allocated := m.TotalAlloc - totalBefore

	t.Logf("权重加载：累计分配 %d MB，GC 后驻留 %d MB",
		allocated/1024/1024, resident/1024/1024)
	t.Logf("（若累计分配明显高于驻留，说明加载过程存在可回收的中间副本）")

	if w == nil {
		t.Fatal("权重为空")
	}
	const maxAcceptable = 220 << 20 // 220 MB
	if allocated > maxAcceptable {
		t.Errorf("加载过程累计分配 %d MB，超出低配设备可承受范围", allocated/1024/1024)
	}
}
