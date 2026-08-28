// 跨模块契约：本文件是 repoMcp 各层之间唯一的类型边界。
// 修改此处等于修改模块间协议，必须同步 index.go / repo.go / tools.go 三侧。
package main

import "context"

// Repo 是白名单中的一个仓库（运行时形态，Dir 已解析为绝对路径）。
type Repo struct {
	Name    string // 短名，作为工具参数与结果中的仓库标识
	URL     string // 远端地址
	Ref     string // 跟踪分支，如 main
	Dir     string // 本地工作树绝对路径
	WebBase string // permalink 前缀，如 https://github.com/owner/repo；空则不产出链接
	Include []string
	Exclude []string

	// Slug 是 owner/name 形式的 GitHub 仓库标识；为空表示该仓不提供 issue 能力。
	Slug string
	// IssueRead / IssueWrite 决定该仓向模型暴露哪些 issue 工具。
	// 写能力必须显式开启，且必须配有令牌。
	IssueRead  bool
	IssueWrite bool
	// GHToken 是访问该仓 issue 用的令牌（可能来自全局 githubToken）。
	GHToken string
	// IssueLabels 是允许模型使用的标签白名单；空表示以仓库现有标签为准。
	IssueLabels []string
}

// Issue 是一条 issue。Body 已把 CRLF 规整为 LF。
type Issue struct {
	Number    int
	Title     string
	State     string // open / closed
	Reason    string // state_reason：completed / not_planned / reopened
	Author    string
	Labels    []string
	Comments  int
	CreatedAt string // YYYY-MM-DD
	UpdatedAt string
	URL       string
	Body      string
}

// IssueComment 是 issue 下的一条评论。
type IssueComment struct {
	Author string
	Date   string
	Body   string
}

// IssueQuery 是一次 issue 检索。Text 为空表示按更新时间列出最近的。
type IssueQuery struct {
	Text   string
	State  string // open / closed / all，空视为 open
	Labels []string
	Limit  int
}

// IssueDraft 是待创建的 issue。Body 由工具层按模板渲染，客户端不能直接控制全文。
type IssueDraft struct {
	Title  string
	Body   string
	Labels []string
}

// IssueEdit 是对已有 issue 的修改。State 为空表示不改状态。
type IssueEdit struct {
	State        string // open / closed
	StateReason  string // completed / not_planned
	AddLabels    []string
	RemoveLabels []string
}

// File 是索引中的一个文件快照。Lines 不含行尾换行符。
type File struct {
	Repo  string
	Path  string // 仓库内相对路径，一律 / 分隔
	Lang  string
	Lines []string
}

// Hit 是一条检索证据。Snippet 已包含上下文并带行号前缀，可直接进 LLM 上下文。
type Hit struct {
	Repo    string
	Path    string
	Line    int // 命中主行，1-based
	EndLine int
	Score   float64
	Snippet string
	Why     string // 命中原因，如 "symbol:decode" / "term:retry"
}

// Symbol 是一个定义点。
type Symbol struct {
	Repo      string
	Path      string
	Line      int
	Kind      string // func/method/type/struct/class/interface/trait/enum/const/var
	Name      string
	Signature string
	Doc       string // 紧邻定义上方的文档注释，已去掉注释前缀
}

// SearchQuery 是一次混合检索请求。Repo/Lang/PathGlob 为空表示不过滤。
type SearchQuery struct {
	Text     string
	Repo     string
	Lang     string
	PathGlob string
	K        int
}

// RepoStats 是单仓索引概况，供 repo_overview 使用。
type RepoStats struct {
	Files   int
	Lines   int
	Symbols int
	ByLang  map[string]int // 语言 -> 文件数
}

// Commit 是一条提交记录。
type Commit struct {
	SHA     string
	Author  string
	Date    string
	Subject string
	Body    string
	Files   []string
}

// BlameLine 是一行的归属信息。
type BlameLine struct {
	Line   int
	SHA    string
	Author string
	Date   string
	Text   string
}

// Indexer 是检索层对外契约，由 *Index 实现。实现必须并发安全：
// Replace 与查询方法会被后台同步协程和请求协程并发调用。
type Indexer interface {
	// Replace 原子替换某仓的全部文档与符号。
	Replace(repo string, files []File)
	Search(q SearchQuery) []Hit
	FindSymbol(name, kind, repo string, k int) []Symbol
	// Tree 返回目录结构摘要行（已按重要性裁剪到 maxEntries 条）。
	Tree(repo string, maxEntries int) []string
	File(repo, path string) (File, bool)
	Stats() map[string]RepoStats
}

// Storer 是仓库与 git 层对外契约，由 *Store 实现。
type Storer interface {
	Repos() []*Repo
	Get(name string) (*Repo, bool)
	// Sync 对所有仓执行 clone 或 fetch+reset，返回首个致命错误。
	Sync(ctx context.Context) error
	// Load 读取工作树中所有符合 Include/Exclude 且非二进制的文件。
	Load(r *Repo) ([]File, error)
	// Head 返回当前 commit sha，未就绪时返回空串。
	Head(repo string) string
	// Log 查询提交历史。path 与 grep 均可为空。
	Log(ctx context.Context, repo, path, grep string, n int) ([]Commit, error)
	Blame(ctx context.Context, repo, path string, start, end int) ([]BlameLine, error)
}

// IssueTracker 是 issue 与 PR 层对外契约，由 *GitHub 实现。实现必须并发安全。
// 契约刻意不含任何删除端点：本服务对仓库的写权力上限就是建 issue/PR / 评论 / 改状态与标签。
type IssueTracker interface {
	List(ctx context.Context, r *Repo, q IssueQuery) ([]Issue, error)
	// Get 返回 issue 正文与最近若干条评论。
	Get(ctx context.Context, r *Repo, number int) (Issue, []IssueComment, error)
	Create(ctx context.Context, r *Repo, d IssueDraft) (Issue, error)
	Comment(ctx context.Context, r *Repo, number int, body string) error
	Edit(ctx context.Context, r *Repo, number int, e IssueEdit) (Issue, error)
	// RepoLabels 返回仓库现有标签，用于拦截模型编造的标签。
	RepoLabels(ctx context.Context, r *Repo) ([]string, error)

	// ── PR 读取 ──
	// ListPulls 按 state 列出 PR。state ∈ {open, closed, all}，空视为 open。
	ListPulls(ctx context.Context, r *Repo, state, head, base string, limit int) ([]Pull, error)
	// GetPull 返回 PR 详情（正文含 300 行内 diff 摘要）。
	GetPull(ctx context.Context, r *Repo, number int) (Pull, error)
	// ── PR 写入 ──
	CreatePull(ctx context.Context, r *Repo, head, base, title, body string) (Pull, error)
	// MergePull 合并 PR。仅支持 squash（干净历史，避免合并提交），
	// commit 为空时按 "Merge PR #n: title" 生成。sha 必须是当前 head，防止合并未预期的提交。
	MergePull(ctx context.Context, r *Repo, number int, sha, commitMsg string) (PullMergeResult, error)
}

// Pull 是一条 Pull Request。Body 已把 CRLF 规整为 LF。
type Pull struct {
	Number      int
	Title       string
	State       string // open / closed / merged
	Author      string
	HeadRef     string // 源分支
	BaseRef     string // 目标分支
	HeadSHA     string
	Additions   int
	Deletions   int
	Commits     int
	Files       int
	Labels      []string
	Comments    int
	CreatedAt   string
	UpdatedAt   string
	URL         string
	Body        string
	// DiffSummary 是文件级差异摘要（path ±lines），仅 GetPull 时填充。
	DiffSummary []PullFile
}

// PullFile 是单个文件的 diff 摘要行，输出紧凑供模型快速感知改动面。
type PullFile struct {
	Path      string
	Status    string // added / modified / removed / renamed
	Additions int
	Deletions int
	Previous  string // renamed 时的旧路径
}

// PullMergeResult 是合并结果。SHA 空时 GitHub 返回非 2xx，Merged=false。
type PullMergeResult struct {
	Merged bool
	SHA    string // 合并生成的提交 sha（squash merge 时是 squash commit）
	Message string
}
