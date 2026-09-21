package engine

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// NativeEngineName 是内嵌 Go 引擎对外显示的名字。
const NativeEngineName = "PandaEngine"

// NativeEngine 是用 Go 重写并内嵌的引擎（实现在 internal/search）。
//
// 与 UCIEngine 的关键差别：它不需要外部可执行文件，因此运行期不需要任何
// 文件执行权限 —— 这正是「皮卡鱼可用: false」那类问题的根源（应用以非 root
// 运行，却要给引擎文件补可执行位）。
//
// 唯一的运行期外部依赖是 NNUE 权重（展开格式约 68 MB），且只读、不改权限。
type NativeEngine struct {
	weightsPath string
	threads     int

	once  sync.Once
	w     *nnue.Weights
	pool  *search.Pool
	err   error
	ready atomic.Bool

	// mu 串行化搜索请求：一个 Pool 内部已按线程数并发，同时跑两个搜索只会
	// 互相抢核。本地单机应用几乎不会出现并发思考，串行足够。
	mu  sync.Mutex
	rng *rand.Rand
}

// NewNativeEngine 构造内嵌引擎。weightsPath 为空时在首次使用时自动探测；
// threads <= 0 时按 CPU 核数自动探测。
func NewNativeEngine(weightsPath string, threads int) *NativeEngine {
	return &NativeEngine{
		weightsPath: weightsPath,
		threads:     threads,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// load 在首次使用时加载权重并建好搜索池（幂等）。
//
// 延迟加载是有意的：权重近 68 MB，解析要一秒左右，而绝大多数会话用不到
// 高强度搜索 —— 放在启动路径上会白白拖慢服务启动。
func (e *NativeEngine) load() error {
	e.once.Do(func() {
		path := e.weightsPath
		if path == "" {
			path = findWeights()
		}
		if path == "" {
			e.err = fmt.Errorf("未找到 NNUE 权重文件")
			return
		}
		w, err := nnue.Load(path)
		if err != nil {
			e.err = fmt.Errorf("加载权重 %s 失败: %w", path, err)
			return
		}
		e.w = w
		e.weightsPath = path
		e.pool = search.NewPool(w, e.threads, search.DefaultTTSizeMB)
		e.ready.Store(true)
	})
	return e.err
}

// candidateWeightPaths 按优先级列出权重候选路径。
//
// 与 candidatePaths（引擎二进制）同样的思路：显式配置 > 可执行文件同目录
// > 可执行文件同目录的 engines/ 子目录 > 当前工作目录。
func candidateWeightPaths(cfgPath string) []string {
	const name = "pikafish.nnue.flat"
	list := []string{}
	if cfgPath != "" {
		list = append(list, cfgPath)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath2(exe)
		list = append(list,
			dir+"/"+name,
			dir+"/engines/"+name,
			dir+"/models/"+name,
		)
	}
	list = append(list, "engines/"+name, name)
	return list
}

// findWeights 返回第一个存在的权重路径；都没有则返回空串。
func findWeights() string {
	seen := map[string]bool{}
	for _, p := range candidateWeightPaths("") {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// Name 返回引擎名。
func (e *NativeEngine) Name() string { return NativeEngineName }

// Available 报告权重文件是否存在（**不触发加载**）。
//
// 调用方（如启动日志与状态接口）在首次搜索前就要知道内嵌引擎能不能用，
// 所以这里只看文件在不在；文件损坏的情况由首次搜索失败后降级处理。
//
// 注意必须实际检查文件：配置里写了一个不存在的路径时，光看「路径非空」
// 会误报可用。
func (e *NativeEngine) Available() bool {
	if e.ready.Load() {
		return true
	}
	if e.weightsPath != "" {
		return fileExists(e.weightsPath)
	}
	return findWeights() != ""
}

// Ready 返回权重是否已加载完成（不代表可用，需配合 Err）。
func (e *NativeEngine) Ready() bool { return e.ready.Load() }

// WeightsPath 返回实际使用的权重路径（未加载时为空）。
func (e *NativeEngine) WeightsPath() string {
	if !e.ready.Load() {
		return ""
	}
	return e.weightsPath
}

// Threads 返回搜索线程数（未加载时为 0）。
func (e *NativeEngine) Threads() int {
	if !e.ready.Load() {
		return 0
	}
	return e.pool.Threads()
}

// ctxErr 在 ctx 已取消或超时时返回对应错误；ctx 为 nil 时返回 nil。
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// lockCtx 获取 e.mu，并在**等待期间**响应 ctx 取消。
//
// 直接 e.mu.Lock() 有个体验上的洞：等锁时取消不生效。用户「取消 → 立刻重下」时，
// 后一个请求会先卡在前一次搜索的锁上，取消按钮形同虚设 —— 要等前一次算完才返回。
// 这里把等待变成可取消的：
//
//	快路径   无竞争时一次 TryLock 就拿到（生产路径几乎都走这条，无额外开销）
//	慢路径   每毫秒试一次，同时听 ctx.Done() ⇒ 取消最多延迟 1ms
//
// 用轮询而不是 channel 信号量，是为了不动 e.mu 的其它使用者（Close /
// ClearTables / Diagnostics 都直接 Lock）—— 换掉锁的原语改造面会大得多。
//
// ⚠️ 拿到锁之后调用方仍应复查 ctxErr(ctx)：TryLock 成功与返回之间仍可能被取消，
// 那时候没必要再白跑一次搜索。
func (e *NativeEngine) lockCtx(ctx context.Context) error {
	if ctx == nil {
		e.mu.Lock()
		return nil
	}
	if e.mu.TryLock() {
		return nil
	}
	t := time.NewTicker(time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if e.mu.TryLock() {
				return nil
			}
		}
	}
}

// BestMove 按档位搜索并返回着法。
//
// ctx 取消（对手超时、连接断开等）会中断搜索并返回错误，而不是降级继续算 ——
// 上层已经不要这个结果了，再耗几秒 CPU 没有意义。
func (e *NativeEngine) BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	if err := e.load(); err != nil {
		return game.Move{}, err
	}
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	// 搜索会大量 Make/Unmake，先用副本，避免改动调用方的局面。
	p := pos.Clone()

	if err := e.lockCtx(ctx); err != nil {
		return game.Move{}, err
	}
	defer e.mu.Unlock()
	if err := ctxErr(ctx); err != nil { // 等锁期间可能已被取消，别再白跑一次
		return game.Move{}, err
	}

	stop := e.watchCancel(ctx)
	defer stop()

	mv, _ := e.pool.SearchAtLevel(p, level, e.rng)
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	if mv == (game.Move{}) {
		return game.Move{}, fmt.Errorf("引擎未给出着法")
	}
	return mv, nil
}

// BestMoveTimed 用固定思考时间搜索，供跨引擎**等时公平对拍**使用。
//
// 走"不限深度、只受时间约束"的路径（等价于最高档但不带随机挑选）：两套引擎的
// 档位参数表并不通用（皮卡鱼调 Skill Level，内嵌引擎调深度与随机池），
// 只有固定思考时间才是可比的口径。
//
// 与 BestMove 一样保留置换表 —— 跨步复用本身就是棋力的一部分，皮卡鱼也这么做。
// BestMoveObserve 是 BestMove 带「迭代观察器」的版本：每次完整迭代完成后回调
// 一次（obs 为 nil 时与 BestMove 完全等价）。
//
// ⚠️ 回调在**搜索线程**上执行：必须自己保证并发安全，且不能反过来调用本引擎。
// 典型用法是把深度/分值/节点数发给 UI，不要在回调里做重活。
func (e *NativeEngine) BestMoveObserve(ctx context.Context, pos *game.Position, level int, obs func(search.Result)) (game.Move, error) {
	if err := e.load(); err != nil {
		return game.Move{}, err
	}
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	p := pos.Clone()

	if err := e.lockCtx(ctx); err != nil {
		return game.Move{}, err
	}
	defer e.mu.Unlock()
	if err := ctxErr(ctx); err != nil { // 等锁期间可能已被取消
		return game.Move{}, err
	}

	stop := e.watchCancel(ctx)
	defer stop()

	mv, _ := e.pool.SearchAtLevelObserve(p, level, e.rng, obs)
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	if mv == (game.Move{}) {
		return game.Move{}, fmt.Errorf("引擎未给出着法")
	}
	return mv, nil
}

func (e *NativeEngine) BestMoveTimed(ctx context.Context, pos *game.Position, movetime time.Duration) (game.Move, error) {
	res, err := e.SearchTimed(ctx, pos, movetime)
	return res.Best, err
}

// SearchTimed 是 BestMoveTimed 的完整结果版，额外给出搜索深度与节点数，
// 供对拍工具量化「同等时间下能搜多深」以及上层做诊断展示。
func (e *NativeEngine) SearchTimed(ctx context.Context, pos *game.Position, movetime time.Duration) (search.Result, error) {
	return e.SearchTimedObserve(ctx, pos, movetime, nil)
}

// SearchTimedObserve 是 SearchTimed 带「迭代观察器」的版本：每次完整迭代完成后
// 回调一次（obs 为 nil 时与 SearchTimed 完全等价）。
//
// ⚠️ 回调在**搜索线程**上执行：它必须自己保证并发安全，且不能反过来调用本引擎
// （会破坏搜索状态）。典型用法是把结果发给 UI，不要在回调里做重活。
func (e *NativeEngine) SearchTimedObserve(ctx context.Context, pos *game.Position, movetime time.Duration, obs func(search.Result)) (search.Result, error) {
	if err := e.load(); err != nil {
		return search.Result{}, err
	}
	if err := ctxErr(ctx); err != nil {
		return search.Result{}, err
	}
	p := pos.Clone()

	if err := e.lockCtx(ctx); err != nil {
		return search.Result{}, err
	}
	defer e.mu.Unlock()
	if err := ctxErr(ctx); err != nil { // 等锁期间可能已被取消
		return search.Result{}, err
	}

	stop := e.watchCancel(ctx)
	defer stop()

	// 观察器只在这次搜索期间挂上：池是共享的，不摘掉会污染下一次调用。
	e.pool.SetIterObserver(obs)
	defer e.pool.SetIterObserver(nil)

	res := e.pool.SearchTime(p, search.TimeLimit{Soft: movetime * 6 / 10, Hard: movetime}, 0)
	if err := ctxErr(ctx); err != nil {
		return search.Result{}, err
	}
	if res.Best == (game.Move{}) {
		return search.Result{}, fmt.Errorf("引擎未给出着法")
	}
	return res, nil
}

// ClearTables 清空置换表与启发式表。换局时调用，避免上一局的搜索经验干扰；
// 同一局内连续走子**不要**调用（清掉会白白损失棋力）。
func (e *NativeEngine) ClearTables() {
	if !e.ready.Load() {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pool != nil {
		e.pool.Clear()
	}
}

// Warmup 提前加载权重。
//
// 对拍/基准测试前调用：否则第一次搜索要把 68 MB 权重的解析时间（约 1 秒）
// 算进思考时间，让对手白占便宜。
func (e *NativeEngine) Warmup() error { return e.load() }

// RankedMoves 返回引擎评估排序的前 n 个候选着法，供 LLM 候选模式使用。
//
// 引擎的根节点本来就会给每个着法打分（低档位的随机挑选就靠这个），
// 所以这里直接取排序结果，比另建一套排序更准。
func (e *NativeEngine) RankedMoves(ctx context.Context, pos *game.Position, level, n int) []game.Move {
	if n <= 0 {
		return nil
	}
	if err := e.load(); err != nil || ctxErr(ctx) != nil {
		return nil
	}
	p := pos.Clone()

	if err := e.lockCtx(ctx); err != nil {
		return nil
	}
	defer e.mu.Unlock()
	if ctxErr(ctx) != nil { // 等锁期间可能已被取消
		return nil
	}

	stop := e.watchCancel(ctx)
	defer stop()

	lp := search.Level(level)
	res := e.pool.SearchTime(p, search.TimeLimit{Soft: lp.Time * 6 / 10, Hard: lp.Time}, lp.MaxDepth)
	if ctxErr(ctx) != nil {
		return nil
	}
	out := make([]game.Move, 0, n)
	for _, rm := range res.Roots {
		if len(out) >= n {
			break
		}
		out = append(out, rm.Move)
	}
	return out
}

// watchCancel 把 context 取消转成引擎的中止信号，返回结束监听的函数。
//
// ⚠️ 返回的函数必须**等监听协程真正退出**再返回，不能只 close(done)。
//
// 否则存在与计时器同款的一条路径（见 search/timemgr.go 的说明）：若 ctx 恰好在
// 搜索结束的瞬间被取消，`ctx.Done()` 与 `done` 同时就绪，select 随机选中前者
// ⇒ 这个 pool.Stop() 落在调用方返回**之后**，而调用方下一次搜索开始时会
// `pool.run` 里的 `stop.Store(false)`。那次迟到的 Stop() 于是打在下一次搜索上。
//
// 这条路径比计时器那条更容易踩到：「用户点取消 → 搜索结束 → 立刻重下」
// 恰好制造「取消与结束同时到达」。
//
// 返回的闭包在调用处是 `defer stop()`，而 `defer e.mu.Unlock()` 注册在它之前 ——
// LIFO 保证 stop() 先执行，于是等协程退出这段时间**仍持有 e.mu**，
// 别的搜索不可能挤进来。pool.Stop() 只是原子写、不取锁，因此不会死锁。
func (e *NativeEngine) watchCancel(ctx context.Context) func() {
	if ctx == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			e.pool.Stop()
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// Diagnostics 返回引擎状态，供启动日志与 /api/status 输出。
func (e *NativeEngine) Diagnostics() []string {
	if !e.ready.Load() {
		return []string{"内嵌 Go 引擎: 权重未加载（首次使用时加载）"}
	}
	// 把指令集路径一并报出来：低配设备上排查「引擎起不来 / 跑得异常慢」
	// 时，第一件要确认的就是它走的是 AVX2 还是标量兜底。
	return []string{fmt.Sprintf("内嵌 Go 引擎: 就绪（权重 %s，%d 线程，置换表 %d MB，累加器内核 %s）",
		e.weightsPath, e.pool.Threads(), search.DefaultTTSizeMB, nnue.SIMDStatus())}
}

// Close 释放权重与搜索池占用的内存。
func (e *NativeEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pool = nil
	e.w = nil
	e.ready.Store(false)
}
