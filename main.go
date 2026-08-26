// repoMcp —— 给 IM 机器人 / MCP 客户端用的仓库源码检索 MCP 服务。
//
// 让聊天机器人里的 LLM 能够检索白名单仓库的源码、符号与提交历史，
// 并带着「路径:行号 + 钉住 commit 的链接」回答问题，答案可被人工核验。
//
// 传输：无状态 Streamable HTTP，端点 POST /mcp（Bearer 鉴权），
// 在 MCP 客户端中按 mode=http（或 remote 自动探测）接入即可。
//
// 检索：本地 clone + 内存倒排索引（BM25）+ 正则符号表。不依赖 embedding、
// 不依赖外部检索服务，全部标准库实现，可 CGO_ENABLED=0 交叉编译为单二进制。
//
// 命令行：
//
//	repomcp -version
//	repomcp -print-config -config config.json   // 脱敏打印最终生效配置并退出
//	repomcp -check-config -config config.json   // 只校验配置并输出摘要，不启动服务
//	repomcp -config config.json                 // 正常启动服务
//
// 环境变量覆盖：REPOMCP_CONFIG / REPOMCP_LISTEN / REPOMCP_TOKEN / REPOMCP_DATA / REPOMCP_GITHUB_TOKEN
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 服务标题与版本号；serverVersion 可通过构建期 ldflags 注入：
//
//	go build -ldflags="-X main.serverVersion=v1.2.3" .
const defaultServerTitle = "SantaChains RepoMcp Service"

var (
	serverTitle   = defaultServerTitle
	serverVersion = "dev"
)

// Server 持有全部运行时依赖。索引、仓库与 issue 层各自保证并发安全，Server 本身无状态。
type Server struct {
	cfg           *Config
	cfgPath       string
	sensitivePats []string // 传给 SanitizeError 的额外 token 列表（全局+仓级）。
	store         *Store
	index         *Index
	gh            *GitHub
	limiter       *issueRateLimiter
	sf            *singleFlight // 手动同步的防重复执行（singleflight）。

	statsMu      sync.RWMutex
	lastSyncDur  time.Duration // 最近一次同步实际耗时
	lastSyncEnd  time.Time     // 最近一次同步结束时刻（UTC）
	nextSchedule time.Time     // 下次定时同步的计划时刻（UTC）
}

// ---- singleflight：避免 /sync 被并发请求触发多次同步 ----

type sfCall struct {
	wg  sync.WaitGroup
	err error
}

// singleFlight 是最小化的 singleflight：相同 key 的并发 Do 共享一次 fn 结果。
// 不引入 golang.org/x/sync，保持零第三方依赖。
type singleFlight struct {
	mu sync.Mutex
	m  map[string]*sfCall
}

func (sf *singleFlight) Do(key string, fn func() error) (err error, shared bool) {
	sf.mu.Lock()
	if c, ok := sf.m[key]; ok {
		sf.mu.Unlock()
		c.wg.Wait()
		return c.err, true
	}
	c := &sfCall{}
	c.wg.Add(1)
	sf.m[key] = c
	sf.mu.Unlock()

	defer func() {
		c.wg.Done()
		sf.mu.Lock()
		delete(sf.m, key)
		sf.mu.Unlock()
	}()
	c.err = fn()
	return c.err, false
}

// lastSyncStats 返回最近同步统计（用于 /healthz / /sync 响应）。nextIn 为负表示未排期。
func (s *Server) lastSyncStats() (dur time.Duration, lastEnd time.Time, nextIn time.Duration) {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	dur = s.lastSyncDur
	lastEnd = s.lastSyncEnd
	if s.nextSchedule.IsZero() {
		nextIn = -1
	} else {
		nextIn = time.Until(s.nextSchedule)
	}
	return
}

func main() {
	flagCfgPath := flag.String("config", "config.json", "配置文件路径")
	flagVersion := flag.Bool("version", false, "打印服务标题与版本号后立即退出")
	flagPrintCfg := flag.Bool("print-config", false, "打印最终生效配置（敏感字段脱敏）为 JSON 后退出")
	flagCheckCfg := flag.Bool("check-config", false, "仅校验配置合法性并打印摘要，不启动服务")
	flag.Parse()

	// （1）-version 优先处理。
	if *flagVersion {
		fmt.Fprintf(os.Stdout, "%s %s\n", serverTitle, serverVersion)
		os.Exit(0)
	}

	// （2）-print-config：读配置 -> 脱敏 -> 打印 JSON -> 退出。
	if *flagPrintCfg {
		cfg, err := LoadConfig(*flagCfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "配置错误：%v\n", err)
			os.Exit(1)
		}
		data, err := MaskedConfigJSON(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "脱敏失败：%v\n", err)
			os.Exit(1)
		}
		os.Stdout.Write(data)
		os.Stdout.Write([]byte("\n"))
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[repomcp] ")

	cfg, err := LoadConfig(*flagCfgPath)
	if err != nil {
		log.Fatalf("配置错误：%v", err)
	}

	// 强校验：失败打印全部具体错误后以非零码退出。
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "配置校验失败：")
		errs := unwrapJoinErrs(err)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		os.Exit(2)
	}

	repos, err := cfg.BuildRepos()
	if err != nil {
		log.Fatalf("配置错误：%v", err)
	}

	// （3）-check-config：沿用原有 checkConfig 函数。
	if *flagCheckCfg {
		checkConfig(cfg, repos)
		return
	}

	if cfg.Token == "" {
		log.Printf("警告：未设置 token，MCP 端点无鉴权。仅应在受信网络或 127.0.0.1 上这样运行。")
	} else {
		log.Printf("Token 已配置（%s）。", maskShort(cfg.Token, 4, 4))
	}

	sensitivePats := collectSensitiveTokens(cfg)

	srv := &Server{
		cfg:           cfg,
		cfgPath:       *flagCfgPath,
		sensitivePats: sensitivePats,
		store:         NewStore(repos),
		index:         NewIndex(),
		gh:            NewGitHub(cfg.GitHubAPIBase, cfg.ghTimeout),
		limiter:       newIssueRateLimiter(cfg.issueLimit),
		sf:            &singleFlight{m: make(map[string]*sfCall)},
	}
	CheckFilePermission(*flagCfgPath, log.Printf)
	logIssueSetup(repos, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", srv.handleMCP)
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/sync", srv.handleSync)
	mux.HandleFunc("/", srv.handleRoot)

	// recoverMiddleware：处理 handler 层的 panic；/mcp 返回 JSON-RPC -32603，其它端点 500。
	recoverMiddleware := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("panic 捕获于 %s %s: %v", r.Method, r.URL.Path, rec)
					if r.URL.Path == "/mcp" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(map[string]any{
							"jsonrpc": "2.0",
							"id":      nil,
							"error": map[string]any{
								"code":    -32603,
								"message": "internal error",
							},
						})
						return
					}
					http.Error(w, "server error", http.StatusInternalServerError)
				}
			}()
			h.ServeHTTP(w, r)
		})
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           recoverMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// 工具调用可能触发 git 子进程，写超时需宽于 gitTimeout。
		WriteTimeout:   cfg.gitTimeout + 30*time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB：防止恶意 header flood。
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.syncLoop(ctx)
	}()

	srvErr := make(chan error, 1)
	go func() {
		log.Printf("监听 %s，仓库 %d 个，端点 POST /mcp", cfg.Listen, len(repos))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	var done bool
	select {
	case <-ctx.Done():
		log.Printf("收到退出信号，正在关闭…")
		done = true
	case err := <-srvErr:
		log.Fatalf("HTTP 服务退出：%v", err)
	}

	if done {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// 先停 HTTP：拒绝新请求并等待现有请求完成。
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP 关闭超时：%v", err)
		}
		// 再通知 syncLoop 尽早退出（虽然 ctx 已取消，这里标记确保不在 wait 里卡死）。
		srv.store.Shutdown()
		syncDone := make(chan struct{})
		go func() {
			wg.Wait()
			close(syncDone)
		}()
		select {
		case <-syncDone:
			log.Printf("同步循环已停止")
		case <-shutdownCtx.Done():
			log.Printf("同步循环停止超时，强制退出")
		}
	}
}

// logIssueSetup 把 issue 能力的实际生效情况打到日志。
// 这类「配置写了但没生效」的问题排查成本极高，启动时讲清楚最省事。
func logIssueSetup(repos []*Repo, cfg *Config) {
	var read, write []string
	for _, r := range repos {
		if !r.IssueRead {
			continue
		}
		if r.IssueWrite {
			write = append(write, r.Name+"="+r.Slug)
			continue
		}
		read = append(read, r.Name+"="+r.Slug)
	}
	if len(read) == 0 && len(write) == 0 {
		log.Printf("issue 工具未启用（没有仓库配置 issues 段）")
		return
	}
	if len(read) > 0 {
		log.Printf("issue 只读：%s", strings.Join(read, " "))
	}
	if len(write) > 0 {
		limit := "不限"
		if cfg.issueLimit > 0 {
			limit = strconv.Itoa(cfg.issueLimit) + " 个/小时"
		}
		log.Printf("issue 可写：%s（创建上限 %s）", strings.Join(write, " "), limit)
	}
	for _, r := range repos {
		if r.IssueRead && r.GHToken == "" {
			log.Printf("警告：仓库 %s 的 issue 未配置令牌，只能读公开仓且限流严格（60 次/小时）", r.Name)
		}
	}
}

// syncLoop 启动时立即同步一次，之后按配置周期重复。
// 服务在首次索引完成前即可接受请求，工具会返回「索引进行中」而非空结果。
func (s *Server) syncLoop(ctx context.Context) {
	if s.store.IsShutdown() {
		return
	}
	s.syncOnce(ctx)
	if s.cfg.syncInterval > 0 {
		s.statsMu.Lock()
		s.nextSchedule = time.Now().UTC().Add(s.cfg.syncInterval)
		s.statsMu.Unlock()
	}
	if s.cfg.syncInterval <= 0 {
		log.Printf("已关闭定时同步（syncInterval=0）")
		return
	}
	t := time.NewTicker(s.cfg.syncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.store.IsShutdown() {
				return
			}
			s.syncOnce(ctx)
			s.statsMu.Lock()
			s.nextSchedule = time.Now().UTC().Add(s.cfg.syncInterval)
			s.statsMu.Unlock()
		}
	}
}

// syncOnce 拉取远端并重建索引。单仓失败被隔离：其余仓库照常索引。
func (s *Server) syncOnce(ctx context.Context) {
	repos := s.store.Repos()
	// 每仓一份 git 超时预算，外加一份余量，避免慢仓拖垮整轮同步。
	deadline := s.cfg.gitTimeout * time.Duration(len(repos)+1)
	syncCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	started := time.Now()
	if err := s.store.Sync(syncCtx); err != nil {
		log.Printf("同步存在失败：%v", err)
	}

	for _, r := range repos {
		if syncCtx.Err() != nil {
			log.Printf("同步超时，剩余仓库本轮跳过")
			break
		}
		t0 := time.Now()
		files, err := s.store.Load(r)
		if err != nil {
			log.Printf("加载 %s 失败：%v", r.Name, err)
			continue
		}
		s.index.Replace(r.Name, files)
		st := s.index.Stats()[r.Name]
		log.Printf("已索引 %s @%s：%d 文件 / %d 行 / %d 符号（%s）",
			r.Name, shortSHA(s.store.Head(r.Name)), st.Files, st.Lines, st.Symbols,
			time.Since(t0).Round(time.Millisecond))
	}
	dur := time.Since(started)
	log.Printf("本轮同步完成，耗时 %s", dur.Round(time.Millisecond))
	s.statsMu.Lock()
	s.lastSyncDur = dur
	s.lastSyncEnd = time.Now().UTC()
	s.statsMu.Unlock()
}

// handleSync 手动触发同步（POST /sync，Bearer 鉴权同 MCP 端点）。
//
// 请求体（可选）：{"blocking": true/false}
//   - blocking=true ：同步完成后返回（200），响应包含 ok/error、duration_ms、是否合并共享。
//   - blocking=false（默认）：无正在执行的同步则立即调度返回 202；已在跑则 202 status=running。
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	setCORS(w, r)
	if !s.authorize(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="repo-mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var p struct {
		Blocking bool `json:"blocking"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
	}

	// 手动同步的超时预算：和 syncOnce 相同（每仓 gitTimeout + 1 余量 + 10 秒缓冲）。
	budget := s.cfg.gitTimeout * time.Duration(len(s.store.Repos())+1)
	syncCtx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()

	fn := func() error {
		s.syncOnce(syncCtx)
		// nextSchedule：手动触发后，下次计划同步按"此刻 + syncInterval"重排。
		if s.cfg.syncInterval > 0 {
			s.statsMu.Lock()
			s.nextSchedule = time.Now().UTC().Add(s.cfg.syncInterval)
			s.statsMu.Unlock()
		}
		return nil
	}

	if p.Blocking {
		sfErr, shared := s.sf.Do("sync", fn)
		dur, lastEnd, nextIn := s.lastSyncStats()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		status := "ok"
		code := http.StatusOK
		if sfErr != nil || syncCtx.Err() != nil {
			status = "error"
			code = http.StatusGatewayTimeout
			if sfErr != nil {
				code = http.StatusInternalServerError
			}
		}
		out := map[string]any{
			"status":        status,
			"shared":        shared,
			"durationMs":    dur.Milliseconds(),
			"lastSyncEnd":   "",
			"nextSyncInSec": int64(-1),
		}
		if !lastEnd.IsZero() {
			out["lastSyncEnd"] = lastEnd.Format("2006-01-02T15:04:05Z")
		}
		if nextIn >= 0 {
			out["nextSyncInSec"] = int64(nextIn.Seconds())
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
		return
	}

	// non-blocking：如果已经在跑则返回 running；否则起后台 goroutine 共享 singleflight。
	s.sf.mu.Lock()
	if _, ok := s.sf.m["sync"]; ok {
		s.sf.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "running"})
		return
	}
	c := &sfCall{}
	c.wg.Add(1)
	s.sf.m["sync"] = c
	s.sf.mu.Unlock()
	go func() {
		defer func() {
			_ = recover() // 防御：sf 内部已记录错误，不用再次 panic
			c.wg.Done()
			s.sf.mu.Lock()
			delete(s.sf.m, "sync")
			s.sf.mu.Unlock()
		}()
		c.err = fn()
	}()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "scheduled"})
}

// checkConfig 以 -check-config 模式输出配置摘要后退出，供部署流水线做启动前校验。
// 只打印必要信息，绝不输出令牌；带凭据的 URL 也一并打码。
func checkConfig(cfg *Config, repos []*Repo) {
	fmt.Printf("配置合法：%d 个仓库\n", len(repos))
	fmt.Printf("监听地址 : %s\n", cfg.Listen)
	fmt.Printf("数据目录 : %s\n", cfg.DataDir)
	fmt.Printf("同步周期 : %s\n", cfg.syncInterval)
	fmt.Printf("git 超时 : %s\n", cfg.gitTimeout)
	ghBase := cfg.GitHubAPIBase
	if strings.TrimSpace(ghBase) == "" {
		ghBase = ghDefaultAPIBase
	}
	fmt.Printf("GitHub   : %s（超时 %s）\n", ghBase, cfg.ghTimeout)
	fmt.Printf("响应预算 : %d 字节\n", cfg.MaxResponseBytes)
	if cfg.issueLimit > 0 {
		fmt.Printf("issue 限频: %d 个/小时/仓\n", cfg.issueLimit)
	} else {
		fmt.Printf("issue 限频: 不限\n")
	}
	for _, r := range repos {
		fmt.Printf("  - %-16s %s ref=%s\n", r.Name, sanitizeGitURL(r.URL), r.Ref)
		fmt.Printf("    dir=%s\n", r.Dir)
		if r.IssueRead {
			mode := "只读"
			if r.IssueWrite {
				mode = "可写"
			}
			fmt.Printf("    issue=%s slug=%s\n", mode, r.Slug)
		}
	}
}

// sanitizeGitURL 打码 URL 中内嵌的凭据（https://user:pass@host/... 或 git@user@host:path），
// 避免令牌泄进 -check-config 的输出或流水线日志。
func sanitizeGitURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 {
			return u[:i+3] + "***@" + rest[at+1:]
		}
	}
	if strings.HasPrefix(u, "git@") {
		rest := u[len("git@"):]
		if at := strings.IndexByte(rest, '@'); at >= 0 {
			return "git@***@" + rest[at+1:]
		}
	}
	return u
}

// unwrapJoinErrs 展开 errors.Join 聚合的错误树，返回叶子错误列表。
func unwrapJoinErrs(err error) []error {
	if err == nil {
		return nil
	}
	var out []error
	type join interface{ Unwrap() []error }
	if j, ok := err.(join); ok {
		for _, e := range j.Unwrap() {
			out = append(out, unwrapJoinErrs(e)...)
		}
		return out
	}
	return []error{err}
}
