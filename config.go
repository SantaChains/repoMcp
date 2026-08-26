// 配置加载：JSON 配置文件 + 环境变量覆盖。零依赖，不引入 YAML 解析。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config 是服务的全部配置。
type Config struct {
	// Listen 监听地址，默认 :8790。
	Listen string `json:"listen"`
	// Token 是 Bearer 鉴权令牌。留空则不鉴权（仅建议在 127.0.0.1 上）。
	Token string `json:"token"`
	// DataDir 存放各仓库的本地 clone，默认 ./data。
	DataDir string `json:"dataDir"`
	// SyncInterval 是后台拉取远端并重建索引的周期，Go duration；"0" 关闭定时同步。
	SyncInterval string `json:"syncInterval"`
	// MaxResponseBytes 是单次工具返回的字节预算，默认 12000。
	// 消费方 MCP 客户端不会截断 tool 返回，必须由本服务自己收口，否则会撑爆 IM 侧小模型上下文。
	MaxResponseBytes int `json:"maxResponseBytes"`
	// GitTimeout 是单条 git 命令的超时，Go duration，默认 3m。
	GitTimeout string `json:"gitTimeout"`

	// GitHubAPIBase 是 GitHub REST API 根地址，默认 https://api.github.com；
	// GitHub Enterprise 填 https://<host>/api/v3。
	GitHubAPIBase string `json:"githubApiBase"`
	// GitHubToken 是 issue 工具使用的默认令牌（PAT，写操作需要 issues:write）；
	// 可被 repos[].issues.token 覆盖，环境变量 REPOMCP_GITHUB_TOKEN 最优先。
	GitHubToken string `json:"githubToken"`
	// GitHubTimeout 是单次 GitHub API 调用超时，Go duration，默认 20s。
	GitHubTimeout string `json:"githubTimeout"`
	// MaxIssueCreatesPerHour 限制单仓每小时可创建的 issue 数，默认 5，0 表示不限。
	// 这是防御性的：模型在对话里反复"帮你提个 issue"是真实风险，提示词约束不住。
	MaxIssueCreatesPerHour *int `json:"maxIssueCreatesPerHour"`

	Repos []RepoConfig `json:"repos"`

	// 解析后的派生值。
	syncInterval time.Duration
	gitTimeout   time.Duration
	ghTimeout    time.Duration
	issueLimit   int // 每仓每小时创建上限，0 = 不限
}

// RepoConfig 是配置文件中的一个仓库条目。
type RepoConfig struct {
	// Name 是短名，LLM 用它作为工具参数。建议全小写无空格。
	Name string `json:"name"`
	// URL 是远端地址；私有仓请在 URL 内嵌 token 或预先配置好凭据助手。
	URL string `json:"url"`
	// Ref 是跟踪分支，默认 main。
	Ref string `json:"ref"`
	// WebBase 是 permalink 前缀（如 https://github.com/owner/repo）。
	// 留空时会尝试从 URL 推导；仍推导不出则结果不含链接。
	WebBase string `json:"webBase"`
	// Dir 可覆盖本地路径；留空则为 <DataDir>/<Name>。
	// 指向一个已存在的本地仓库时，同步阶段仍会 fetch+reset，请勿指向你的开发工作树。
	Dir string `json:"dir"`
	// Desc 是一句话说明，会出现在 repo_overview 里帮助 LLM 选仓。
	Desc string `json:"desc"`

	Include []string `json:"include"`
	Exclude []string `json:"exclude"`

	// Issues 开启该仓的 issue 能力；省略则该仓不出现在任何 issue 工具里。
	Issues *RepoIssuesConfig `json:"issues"`
}

// RepoIssuesConfig 控制单个仓库的 issue 能力。给出空对象 {} 即为「只读检索」。
type RepoIssuesConfig struct {
	// Slug 是 owner/repo；留空则从 webBase / url 推导（仅 GitHub 风格地址可推导）。
	Slug string `json:"slug"`
	// Write 允许创建 issue、评论与改状态；默认 false，即只读。
	Write bool `json:"write"`
	// Token 覆盖全局 githubToken。
	Token string `json:"token"`
	// Labels 是允许模型使用的标签白名单；留空表示以仓库现有标签为准。
	Labels []string `json:"labels"`
}

var reRepoName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// LoadConfig 读取配置文件并应用环境变量覆盖。
// 环境变量：REPOMCP_CONFIG / REPOMCP_LISTEN / REPOMCP_TOKEN / REPOMCP_DATA。
func LoadConfig(path string) (*Config, error) {
	if v := os.Getenv("REPOMCP_CONFIG"); v != "" {
		path = v
	}
	cfg := &Config{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}

	if v := os.Getenv("REPOMCP_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("REPOMCP_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("REPOMCP_DATA"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("REPOMCP_GITHUB_TOKEN"); v != "" {
		cfg.GitHubToken = v
	}

	if cfg.Listen == "" {
		cfg.Listen = ":8790"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 12000
	}
	if cfg.MaxResponseBytes < 2000 {
		return nil, errors.New("maxResponseBytes 不应小于 2000，否则检索结果无法承载证据")
	}

	cfg.syncInterval, err = parseDur(cfg.SyncInterval, 15*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("syncInterval: %w", err)
	}
	cfg.gitTimeout, err = parseDur(cfg.GitTimeout, 3*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("gitTimeout: %w", err)
	}
	if cfg.gitTimeout <= 0 {
		cfg.gitTimeout = 3 * time.Minute
	}
	cfg.ghTimeout, err = parseDur(cfg.GitHubTimeout, 20*time.Second)
	if err != nil {
		return nil, fmt.Errorf("githubTimeout: %w", err)
	}
	if cfg.ghTimeout <= 0 {
		cfg.ghTimeout = 20 * time.Second
	}
	cfg.issueLimit = 5
	if n := cfg.MaxIssueCreatesPerHour; n != nil {
		if *n < 0 {
			return nil, errors.New("maxIssueCreatesPerHour 不能为负；0 表示不限")
		}
		cfg.issueLimit = *n
	}

	if len(cfg.Repos) == 0 {
		return nil, errors.New("repos 为空：至少配置一个仓库")
	}
	return cfg, nil
}

func parseDur(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	if s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

// BuildRepos 把配置条目解析为运行时 Repo（Dir 绝对化、WebBase 推导、重名校验）。
func (c *Config) BuildRepos() ([]*Repo, error) {
	dataDir, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析 dataDir: %w", err)
	}
	seen := make(map[string]bool, len(c.Repos))
	out := make([]*Repo, 0, len(c.Repos))
	for i, rc := range c.Repos {
		name := strings.ToLower(strings.TrimSpace(rc.Name))
		if !reRepoName.MatchString(name) {
			return nil, fmt.Errorf("repos[%d].name %q 非法：要求 ^[a-z0-9][a-z0-9._-]{0,63}$", i, rc.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("repos[%d].name %q 重复", i, name)
		}
		seen[name] = true
		if strings.TrimSpace(rc.URL) == "" {
			return nil, fmt.Errorf("repos[%d] (%s).url 为空", i, name)
		}

		dir := rc.Dir
		if dir == "" {
			dir = filepath.Join(dataDir, name)
		}
		dir, err = filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("repos[%d] (%s).dir: %w", i, name, err)
		}

		ref := rc.Ref
		if ref == "" {
			ref = "main"
		}
		web := strings.TrimRight(rc.WebBase, "/")
		if web == "" {
			web = deriveWebBase(rc.URL)
		}

		r := &Repo{
			Name:    name,
			URL:     rc.URL,
			Ref:     ref,
			Dir:     dir,
			WebBase: web,
			Include: rc.Include,
			Exclude: rc.Exclude,
		}
		if err := c.applyIssues(r, rc, i); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

var reRepoSlug = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// applyIssues 解析单仓的 issue 配置：推导 slug、挑令牌、校验写能力的前置条件。
// 写能力必须有令牌——匿名调用 GitHub 根本写不了，与其运行期报 401，不如启动就拒绝。
func (c *Config) applyIssues(r *Repo, rc RepoConfig, i int) error {
	if rc.Issues == nil {
		return nil
	}
	slug := strings.Trim(strings.TrimSpace(rc.Issues.Slug), "/")
	if slug == "" {
		slug = deriveSlug(r.WebBase, rc.URL)
	}
	if slug == "" {
		return fmt.Errorf("repos[%d] (%s).issues：无法从 url/webBase 推导 owner/repo，请显式填写 issues.slug", i, r.Name)
	}
	if !reRepoSlug.MatchString(slug) {
		return fmt.Errorf("repos[%d] (%s).issues.slug %q 非法：要求 owner/repo 形式", i, r.Name, slug)
	}
	token := strings.TrimSpace(rc.Issues.Token)
	if token == "" {
		token = strings.TrimSpace(c.GitHubToken)
	}
	if rc.Issues.Write && token == "" {
		return fmt.Errorf("repos[%d] (%s).issues.write=true 但无令牌：请配置 githubToken 或 repos[].issues.token", i, r.Name)
	}
	r.Slug = slug
	r.GHToken = token
	r.IssueRead = true
	r.IssueWrite = rc.Issues.Write
	r.IssueLabels = rc.Issues.Labels
	return nil
}

// deriveSlug 从网页前缀（或据 clone 地址推导出的网页前缀）取出 owner/repo。
func deriveSlug(webBase, url string) string {
	base := strings.TrimRight(webBase, "/")
	if base == "" {
		base = deriveWebBase(url)
	}
	if base == "" {
		return ""
	}
	if i := strings.Index(base, "://"); i >= 0 {
		base = base[i+3:]
	}
	// 首段是主机名，其后两段即 owner/repo。
	parts := strings.Split(strings.Trim(base, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	owner, name := parts[1], strings.TrimSuffix(parts[2], ".git")
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// deriveWebBase 从 clone URL 推导网页前缀，支持 https 与 scp 风格 ssh 地址。
// 推导不出（本地路径、自建非常规地址）时返回空串，调用方据此省略 permalink。
func deriveWebBase(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, ".git")
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		// 去掉可能内嵌的凭据，避免 token 被写进给 LLM 的链接里。
		if i := strings.Index(u, "://"); i >= 0 {
			rest := u[i+3:]
			if at := strings.Index(rest, "@"); at >= 0 && at < strings.Index(rest+"/", "/") {
				u = u[:i+3] + rest[at+1:]
			}
		}
		return strings.TrimRight(u, "/")
	case strings.HasPrefix(u, "git@"):
		rest := strings.TrimPrefix(u, "git@")
		host, path, ok := strings.Cut(rest, ":")
		if !ok || host == "" || path == "" {
			return ""
		}
		return "https://" + host + "/" + strings.TrimLeft(path, "/")
	default:
		return ""
	}
}

// desc 按名字取回配置里的一句话说明。
func (c *Config) desc(name string) string {
	for _, rc := range c.Repos {
		if strings.EqualFold(strings.TrimSpace(rc.Name), name) {
			return rc.Desc
		}
	}
	return ""
}

// itoa 供输出格式化使用，避免各处重复导入 strconv。
func itoa(n int) string { return strconv.Itoa(n) }

// maskShort 返回 s 的脱敏形式：保留前 head 后 tail 个字符，中间用 '*' 填充。
// 当 s 长度 <= head+tail 时，返回至少 8 个 '*' 组成的掩码，避免信息暴露。
func maskShort(s string, head, tail int) string {
	if s == "" {
		return "********"
	}
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	runes := []rune(s)
	n := len(runes)
	if n <= head+tail {
		return strings.Repeat("*", maxInt(8, n))
	}
	mid := n - head - tail
	if mid < 4 {
		mid = 4
	}
	return string(runes[:head]) + strings.Repeat("*", mid) + string(runes[n-tail:])
}

// maxInt 返回 a、b 中较大值。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// MaskedConfigJSON 深拷贝 cfg 并对敏感字段脱敏，然后以 json.MarshalIndent 输出。
//
// 脱敏规则：
//  1 cfg.Token / cfg.GitHubToken 保留首 4 尾 4。
//  2 每个 RepoConfig.URL / Issues.Token 同样套用脱敏或凭据清除。
//  3 派生出的 syncInterval/gitTimeout 等内部字段以字符串可见形式还原到输出字段。
func MaskedConfigJSON(cfg *Config) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("config 为空")
	}
	// 深拷贝：序列→反序列化一次（标准库保证字段覆盖）。
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var clone Config
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	// 令牌脱敏。
	clone.Token = maskShort(clone.Token, 4, 4)
	clone.GitHubToken = maskShort(clone.GitHubToken, 4, 4)
	// 每个仓库 URL 去内嵌凭据 + issues.token 脱敏。
	for i := range clone.Repos {
		clone.Repos[i].URL = sanitizeGitURL(clone.Repos[i].URL)
		if clone.Repos[i].Issues != nil {
			clone.Repos[i].Issues.Token = maskShort(clone.Repos[i].Issues.Token, 4, 4)
		}
	}
	// 派生值以可读形式写入同名字段（JSON marshal 时不导出 syncInterval 等未导出内部字段，
	// 故直接用已有字符串字段即可）。
	return json.MarshalIndent(clone, "", "  ")
}

// Validate 执行配置强校验；失败返回 errors.Join 聚合的多错误，错误消息包含字段名与规则。
// T002 阶段占位返回 nil；具体规则在 T003 中实现并生效。
func (cfg *Config) Validate() error {
	if cfg == nil {
		return errors.New("config: 为空")
	}
	var errs []error

	// 规则 1：token 长度 >= 16 且不含空白字符。
	if cfg.Token != "" {
		if len(cfg.Token) < 16 {
			errs = append(errs, fmt.Errorf("config.token: 长度 < 16（实际 %d），要求 >= 16", len(cfg.Token)))
		}
		if strings.ContainsAny(cfg.Token, " \t\n\r") {
			errs = append(errs, errors.New("config.token: 不得包含空白字符（空格/Tab/换行）"))
		}
	}
	// 规则 2：listen 必须是 host:port 合法格式。
	if cfg.Listen != "" {
		host, portStr, err := net.SplitHostPort(cfg.Listen)
		_ = host
		if err != nil {
			errs = append(errs, fmt.Errorf("config.listen %q 格式非法：要求 host:port（%v）", cfg.Listen, err))
		} else {
			port, perr := strconv.Atoi(portStr)
			if perr != nil || port < 1 || port > 65535 {
				errs = append(errs, fmt.Errorf("config.listen: port %q 超出 1..65535 范围", portStr))
			}
		}
	}
	// 规则 3：dataDir 目录必须可写。
	if cfg.DataDir != "" {
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("config.dataDir %q 创建失败：%w", cfg.DataDir, err))
		} else {
			tmp, terr := os.CreateTemp(cfg.DataDir, ".repomcp-writecheck-*")
			if terr != nil {
				errs = append(errs, fmt.Errorf("config.dataDir %q 不可写：%w", cfg.DataDir, terr))
			} else {
				tmp.Close()
				os.Remove(tmp.Name())
			}
		}
	}
	// 规则 4：repos 非空。
	if len(cfg.Repos) == 0 {
		// LoadConfig 已做，但 Validate 兜底避免未来流程绕过。
		errs = append(errs, errors.New("config.repos: 为空，至少配置一个仓库"))
	}
	// 规则 5：每个 repo.Name 合法且唯一。
	if len(cfg.Repos) > 0 {
		seen := make(map[string]int, len(cfg.Repos))
		reName := regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
		for i, r := range cfg.Repos {
			name := strings.TrimSpace(r.Name)
			if name == "" {
				errs = append(errs, fmt.Errorf("config.repos[%d].name: 为空", i))
				continue
			}
			if !reName.MatchString(name) || containsSpaceOrUnprintable(name) {
				errs = append(errs, fmt.Errorf("config.repos[%d].name %q: 仅允许 ASCII 可打印字符（字母数字 ._-），长度 1..64", i, name))
				continue
			}
			if prev, ok := seen[strings.ToLower(name)]; ok {
				errs = append(errs, fmt.Errorf("config.repos[%d].name %q: 与 repos[%d] 重名（大小写不敏感）", i, name, prev))
				continue
			}
			seen[strings.ToLower(name)] = i
			// 规则 6：URL 前缀。
			u := strings.TrimSpace(r.URL)
			if u == "" {
				errs = append(errs, fmt.Errorf("config.repos[%d].url (%s): 为空", i, name))
			} else if !strings.HasPrefix(u, "http://") &&
				!strings.HasPrefix(u, "https://") &&
				!strings.HasPrefix(u, "git@") {
				errs = append(errs, fmt.Errorf("config.repos[%d].url (%s): 必须以 http:// / https:// / git@ 开头", i, name))
			}
		}
	}
	// 规则 7/8：syncInterval / gitTimeout 为正（LoadConfig 已保证 >0，这里只确认派生值）。
	if cfg.syncInterval < 0 {
		errs = append(errs, fmt.Errorf("config.syncInterval: 解析得到负值 %v", cfg.syncInterval))
	}
	if cfg.gitTimeout <= 0 {
		errs = append(errs, fmt.Errorf("config.gitTimeout: 必须 > 0（实际 %v）", cfg.gitTimeout))
	}
	// 规则 9：maxResponseBytes 夹取到 2000..200000。
	if cfg.MaxResponseBytes < 2000 || cfg.MaxResponseBytes > 200000 {
		// 非致命：夹取并记录，但不阻止启动。
		clamped := cfg.MaxResponseBytes
		if clamped < 2000 {
			clamped = 2000
		}
		if clamped > 200000 {
			clamped = 200000
		}
		if clamped != cfg.MaxResponseBytes {
			LogWarnf("config.maxResponseBytes %d 夹取到 %d（允许区间 2000..200000）", cfg.MaxResponseBytes, clamped)
			cfg.MaxResponseBytes = clamped
		}
	}
	// 规则 10：GitHubToken 若是占位 ${...} 形式仅发 warn。
	if strings.HasPrefix(strings.TrimSpace(cfg.GitHubToken), "${") &&
		strings.HasSuffix(strings.TrimSpace(cfg.GitHubToken), "}") {
		LogWarnf("config.githubToken 值 %q 看起来是未替换的环境变量占位，Issue 工具可能降级为只读匿名调用",
			maskShort(cfg.GitHubToken, 2, 2))
	}
	return errors.Join(errs...)
}

// containsSpaceOrUnprintable 判断 name 是否含空白字符或非 ASCII 可打印字符（供 Validate 规则 5 使用）。
func containsSpaceOrUnprintable(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			return true
		}
		if r > unicode.MaxASCII {
			return true
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
