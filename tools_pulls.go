// tools_pulls.go：Pull Request 的检索、创建与合并工具，及服务端护栏。
//
// PR 护栏遵循 issue 的同样设计哲学：凡是能硬校验的都在服务端拦——
//   - 新建 PR 强制先拉一次 GetPull 检查 open 状态的 head/base 是否已经存在；
//   - 合并必须是 squash 且要求给出当前 head.sha，防止合并未预期提交；
//   - 分支名非法直接拒；分支不能与已受保护的 main/master/production/release 等混淆；
//   - 写操作限频与 issue 共享（同 Limiter），create_pull 视同 create_issue。
package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	pullTitleMinRunes  = 10
	pullTitleMaxRunes  = 300
	pullBodyMinRunes   = 20
	pullBodyMaxRunes   = 16000 // PR 正文允许更长（会附改动摘要+测试证据）
	pullMsgMinRunes    = 10
	pullMsgMaxRunes    = 4000
	pullBranchMaxRunes = 80
	// protectedBases 是不允许作为 PR 源分支（head）的分支名——防止模型不小心改了目标分支。
	// 作为 PR 的目标分支（base）时仍然允许。
)

// reBranchName 是 GitHub 可接受的分支名严格子集：
// 只允许小写字母、数字、-、.、_、/，首尾必须是字母数字，禁止 ".."。
var reBranchName = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*[a-z0-9]$`)

// pullProtectedPrefix 禁止作为 head 分支名（避免误改 main/master/生产分支）。
var pullProtectedPrefix = []string{"main", "master", "production", "prod-", "release/", "releases/", "hotfix/", "stable"}

var _ = sort.Search

// isProtectedHead 分支名是否是"受保护基线"，不允许作为 PR 的 head。
func isProtectedHead(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return true
	}
	for _, p := range pullProtectedPrefix {
		if n == p || strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// validateBranchName 校验分支名是否符合允许的命名规范。
// lenient=true 时允许 owner:ref 形式（GitHub head 过滤器语法）。
func validateBranchName(name string, lenient bool) error {
	s := strings.TrimSpace(name)
	if s == "" {
		return errors.New("分支名不能为空")
	}
	if n := utf8.RuneCountInString(s); n > pullBranchMaxRunes {
		return fmt.Errorf("分支名过长：%d 字，上限 %d 字", n, pullBranchMaxRunes)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("分支名 %q 含 '..'，不合法", s)
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".lock") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") {
		return fmt.Errorf("分支名 %q 含不合法前缀或后缀", s)
	}
	// lenient 模式：owner:ref 形式，只校验 ref 段。
	if lenient && strings.Contains(s, ":") {
		parts := strings.SplitN(s, ":", 2)
		if strings.TrimSpace(parts[0]) == "" {
			return fmt.Errorf("分支 %q owner 前缀为空", s)
		}
		s = parts[1]
	}
	if !reBranchName.MatchString(s) {
		return fmt.Errorf("分支名 %q 不合法；只允许小写字母、数字、-._/，首尾必须字母数字", s)
	}
	return nil
}

// ── 工具定义 ────────────────────────────────────────────────

// pullToolDefs 按 issue 能力动态挂载：只读两工具（IssueRead）+ 写两工具（IssueWrite）。
// 与 issue 工具共享读写仓集合，因为 issue 与 PR 都走 GitHub Issue/PR 平台权限。
func (s *Server) pullToolDefs() []toolDef {
	readable := s.issueRepos(false)
	if len(readable) == 0 {
		return nil
	}
	writable := s.issueRepos(true)

	repoDesc := "仓库短名，取值：" + issueRepoList(readable)
	if len(readable) == 1 {
		repoDesc += "（只有一个，可省略）"
	}
	defs := []toolDef{
		{
			Name:  "list_pulls",
			Title: "列出 PR",
			Desc: "列出最近更新的 PR，可按状态、head/base 过滤。" +
				"回答「现在有哪些 PR 开着」「这个分支有没有 PR」「要进 main 的最近改动」时用它。" +
				"注意：PR 和 issue 是两种资源，list_pulls 不会返回 issue；搜历史 PR 改到某文件时用 git_history。",
			Schema: obj(map[string]any{
				"repo":  str(repoDesc),
				"state": str("状态：open（默认）/ closed（关或已合）/ merged（仅合并）/ all。merged 和 closed 不区分时用 closed"),
				"head":  str("只看源分支名是这个的 PR，如 feature/fix-audio；一般不填"),
				"base":  str("只看目标分支名是这个的 PR，如 main / develop"),
				"limit": integer("返回条数，默认 20", 1, 100),
			}),
			Handle: s.toolListPulls,
		},
		{
			Name:  "get_pull",
			Title: "读取 PR 详情",
			Desc: "读取单条 PR 的完整正文、变更统计 + 文件级 diff 摘要（不含每行代码）。" +
				"在 list_pulls 拿到候选后判断「改了哪些文件」「规模多大」「是否值得细看」时用它。" +
				"要看具体改动的源码行：改用 read_file + 对应 commit sha 或 git_history。",
			Schema: obj(map[string]any{
				"number": integer("PR 编号（不带 #）", 1, 1000000),
				"repo":   str(repoDesc),
			}, "number"),
			Handle: s.toolGetPull,
		},
	}
	if len(writable) == 0 {
		return defs
	}
	writeDesc := "仓库短名，取值：" + issueRepoList(writable)
	if len(writable) == 1 {
		writeDesc += "（只有一个，可省略）"
	}
	return append(defs,
		toolDef{
			Name:  "create_pull",
			Title: "创建 PR",
			Desc: "在 GitHub 上把 head 分支合到 base 分支的 Pull Request 创建出来。这是真实写入，" +
				"维护者会收到通知。调用前必须先确认：" +
				"1) head 分支确实存在于远端，且改动内容与 title/body 描述一致；" +
				"2) base 是正确的目标分支（通常 main / master / develop）；" +
				"3) 相同 head→base 没有已经存在的 open PR（先调 list_pulls state=open head=...）；" +
				"4) 用户明确要提交。服务端会查重 + 限频。",
			Schema: obj(map[string]any{
				"head":  str("源分支名，如 feature/fix-login；必须已经推到远端；不能是 main/master 等基线；只允许小写字母数字-._/"),
				"base":  str("目标分支名，如 main / master / develop。默认 main；未填写则用仓库配置的 ref 字段"),
				"title": str("一句话改动标题。写法形如「修复xxx」「新增xxx支持」「重构xxx流程」，不要以句号结尾"),
				"body":  str("正文，建议结构化：背景、改动摘要、测试证据、可能影响范围；至少 20 字"),
				"repo":  str(writeDesc),
			}, "head", "title", "body"),
			Handle: s.toolCreatePull,
		},
		toolDef{
			Name:  "merge_pull",
			Title: "合并 PR",
			Desc: "合并已打开的 PR。采用 squash 合并：把 PR 的所有提交压缩成一条干净历史写入 base。" +
				"调用前必须先 get_pull：确认 PR 是 open 状态、没有冲突（mergeable）；" +
				"把返回的 head_sha 填在 sha 参数里——这能保证不会把中间新到的提交一并合入。" +
				"调用失败会提示原因（冲突、未通过 CI、base/head 保护），这时不要重复尝试。",
			Schema: obj(map[string]any{
				"number":       integer("PR 编号（不带 #）", 1, 1000000),
				"sha":          str("必填：当前 head 的完整 commit sha 或短 sha（8-40 hex）。先 get_pull 拿到 head_sha 再填"),
				"commit_title": str("squash 后的 commit 标题；省略时用 PR 标题"),
				"commit_body":  str("squash 后的正文；省略时按 PR 正文摘要生成"),
				"repo":         str(writeDesc),
			}, "number", "sha"),
			Handle: s.toolMergePull,
		})
}

// renderPullNote 在 PR 正文末尾附加代提交来源信息。
func (s *Server) renderPullNote(kind string, indexed bool, repoName string) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(kind)
	if indexed {
		if head := s.shortHead(repoName); head != "" {
			b.WriteString("，索引 commit `")
			b.WriteString(head)
			b.WriteString("`")
		}
	}
	b.WriteString("。由 SantaChains repoMcp 代用户操作，改动与 PR 正文仅作参考，请以实际审查为准。\n")
	return b.String()
}

// ── list_pulls ─────────────────────────────────────────────

func (s *Server) toolListPulls(ctx context.Context, args map[string]any) (string, error) {
	r, err := s.resolveIssueRepo(args, false)
	if err != nil {
		return "", err
	}
	state := strings.ToLower(strings.TrimSpace(argStr(args, "state")))
	// GitHub REST 没有 merged state——merged=closed；这里做语义转换。
	onlyMerged := false
	switch state {
	case "merged":
		state = "closed"
		onlyMerged = true
	case "open", "closed", "all", "":
	default:
		return "", fmt.Errorf("state 非法：%q。合法值 open/closed/merged/all", state)
	}
	head := strings.TrimSpace(argStr(args, "head"))
	base := strings.TrimSpace(argStr(args, "base"))
	if head != "" {
		if err := validateBranchName(head, false); err != nil {
			return "", fmt.Errorf("head %w", err)
		}
	}
	if base != "" {
		if err := validateBranchName(base, false); err != nil {
			return "", fmt.Errorf("base %w", err)
		}
	}
	limit := argInt(args, "limit", 20)

	ps, err := s.gh.ListPulls(ctx, r, state, head, base, limit)
	if err != nil {
		return "", err
	}
	if onlyMerged {
		filtered := ps[:0]
		for _, p := range ps {
			if p.State == "merged" {
				filtered = append(filtered, p)
			}
		}
		ps = filtered
	}

	w := newBudget(s.cfg.MaxResponseBytes)
	title := fmt.Sprintf("%s PR 列表，state=%s", r.Name, state)
	if onlyMerged {
		title = fmt.Sprintf("%s 已合并的 PR", r.Name)
	}
	if head != "" {
		title += "，head=" + head
	}
	if base != "" {
		title += "，base=" + base
	}
	w.line(title)
	if len(ps) == 0 {
		w.line("无结果。")
		return w.String(), nil
	}
	for i, p := range ps {
		w.line("")
		line := fmt.Sprintf("[%d] #%d  %s  %s", i+1, p.Number, p.State, truncate(p.Title, 240))
		if len(p.Labels) > 0 {
			line += "  [" + strings.Join(p.Labels, ", ") + "]"
		}
		if !w.line(line) {
			break
		}
		w.line(fmt.Sprintf("  %s → %s  +%d -%d  %d 提交 / %d 文件  作者 %s",
			p.HeadRef, p.BaseRef, p.Additions, p.Deletions, p.Commits, p.Files, truncate(p.Author, 20)))
		if p.CreatedAt != "" {
			w.line("  创建 " + p.CreatedAt + "  更新 " + p.UpdatedAt)
		}
		if p.URL != "" {
			w.line("  " + p.URL)
		}
	}
	return w.String(), nil
}

// ── get_pull ───────────────────────────────────────────────

func (s *Server) toolGetPull(ctx context.Context, args map[string]any) (string, error) {
	r, err := s.resolveIssueRepo(args, false)
	if err != nil {
		return "", err
	}
	number := argInt(args, "number", 0)
	if number <= 0 {
		return "", errors.New("number 必须是正整数")
	}
	p, err := s.gh.GetPull(ctx, r, number)
	if err != nil {
		return "", err
	}

	w := newBudget(s.cfg.MaxResponseBytes)
	w.line(fmt.Sprintf("#%d  %s  [%s]", p.Number, truncate(p.Title, 260), p.State))
	if len(p.Labels) > 0 {
		w.line("标签：" + strings.Join(p.Labels, " / "))
	}
	w.line(fmt.Sprintf("源分支 %s → 目标分支 %s", p.HeadRef, p.BaseRef))
	w.line(fmt.Sprintf("head_sha: %s（合并时必须把这个字符串填到 merge_pull.sha）", p.HeadSHA))
	w.line(fmt.Sprintf("规模：+%d  -%d  %d 提交  %d 文件  %d 讨论", p.Additions, p.Deletions, p.Commits, p.Files, p.Comments))
	w.line(fmt.Sprintf("作者 %s  创建 %s  更新 %s", truncate(p.Author, 30), p.CreatedAt, p.UpdatedAt))
	if p.URL != "" {
		w.line("链接：" + p.URL)
	}
	if strings.TrimSpace(p.Body) != "" {
		w.line("")
		w.line("正文：")
		lines := strings.Split(p.Body, "\n")
		for i, l := range lines {
			if i > 60 {
				w.line("  …正文过长，省略剩余 " + itoa(len(lines)-i) + " 行")
				break
			}
			if !w.line("  " + truncate(l, 300)) {
				break
			}
		}
	}
	if len(p.DiffSummary) > 0 {
		w.line("")
		w.line("文件变更（" + itoa(len(p.DiffSummary)) + " 份）：")
		for i, f := range p.DiffSummary {
			if i > 50 {
				w.line("  …还有 " + itoa(len(p.DiffSummary)-i) + " 份文件省略，详细列表去链接里看")
				break
			}
			line := fmt.Sprintf("  %s  %s  +%d  -%d", f.Status, f.Path, f.Additions, f.Deletions)
			if f.Previous != "" {
				line += "  ← " + f.Previous
			}
			if !w.line(line) {
				break
			}
		}
	}
	return w.String(), nil
}

// ── create_pull ────────────────────────────────────────────

// pullDupThreshold 判定「同一 head→base 已存在 open PR」的宽松阈值。
// PR 查重不做词法重叠：head/base 对重复就是重复（这是精确查重）。
const pullDupThresholdIssueWindow = 40 // 先拉最近 40 条 open PR 看是否有同一 head→base

func (s *Server) toolCreatePull(ctx context.Context, args map[string]any) (string, error) {
	r, err := s.resolveIssueRepo(args, true)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(argStr(args, "head"))
	base := strings.TrimSpace(argStr(args, "base"))
	title := strings.TrimSpace(argStr(args, "title"))
	body := strings.TrimSpace(argStr(args, "body"))

	if base == "" {
		base = r.Ref
	}
	if base == "" {
		base = "main"
	}

	// 基本格式校验
	if err := validateBranchName(head, false); err != nil {
		return "", fmt.Errorf("head %w", err)
	}
	if isProtectedHead(head) {
		return "", fmt.Errorf("head 不能是基线分支 %q（会污染主要分支）。请先建一个特性分支再提 PR", head)
	}
	if err := validateBranchName(base, false); err != nil {
		return "", fmt.Errorf("base %w", err)
	}
	if strings.EqualFold(head, base) {
		return "", fmt.Errorf("head 与 base 都是 %q，PR 没有意义", head)
	}

	if n := utf8.RuneCountInString(title); n < pullTitleMinRunes || n > pullTitleMaxRunes {
		return "", fmt.Errorf("title 长度需在 %d–%d 字，当前 %d 字", pullTitleMinRunes, pullTitleMaxRunes, n)
	}
	if n := utf8.RuneCountInString(body); n < pullBodyMinRunes {
		return "", fmt.Errorf("body 太短（不足 %d 字）：请写清背景、改动摘要和测试依据", pullBodyMinRunes)
	} else if n > pullBodyMaxRunes {
		return "", fmt.Errorf("body 过长（%d 字），上限 %d 字；次要信息移到外链或压缩", n, pullBodyMaxRunes)
	}

	// 查重：同 head→base 已存在 open PR。
	exists, err := s.gh.ListPulls(ctx, r, "open", head, base, pullDupThresholdIssueWindow)
	if err == nil && len(exists) > 0 {
		candidates := make([]string, 0, len(exists))
		for _, e := range exists {
			candidates = append(candidates, fmt.Sprintf("#%d", e.Number))
		}
		return "", fmt.Errorf("同一 head=%s → base=%s 已经存在 open PR：%s。先读现有 PR 判断是否要更新，不要重复开单",
			head, base, strings.Join(candidates, ", "))
	}

	// 限频：create_pull 和 create_issue 共享同一限流桶（配额宝贵）。
	if ok, wait := s.limiter.take(r.Name, "PR"); !ok {
		return "", fmt.Errorf("未创建：%s 已达创建上限（单仓 %d/小时 / 全局 %d/小时），约 %d 分钟后可再试",
			r.Name, s.cfg.issueLimit, issueGlobalPerHourSoft, int(wait.Minutes())+1)
	}

	// 按模板追加签名，防止模型把正文当普通 issue 写——PR 里要能看出代提交来源。
	bodyFinal := body + "\n\n---\n" + s.renderPullNote("PR（代用户提交）", true, "")

	p, err := s.gh.CreatePull(ctx, r, head, base, title, bodyFinal)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "已创建 PR #%d\n", p.Number)
	fmt.Fprintf(&sb, "标题：%s\n", p.Title)
	fmt.Fprintf(&sb, "分支：%s → %s\n", p.HeadRef, p.BaseRef)
	if p.URL != "" {
		fmt.Fprintf(&sb, "链接：%s\n", p.URL)
	}
	if p.State != "open" {
		fmt.Fprintf(&sb, "状态：%s\n", p.State)
	}
	sb.WriteString("\n")
	sb.WriteString("后续：维护者可能在 PR 下评论，到读 PR 用 get_pull，合并用 merge_pull（带 head_sha）。")
	return sb.String(), nil
}

// ── merge_pull ─────────────────────────────────────────────

// reFullSHA 匹配完整 40 位十六进制 sha；merge_pull 的 sha 参数可以传短 sha（8-40）。
var reHexSHA = regexp.MustCompile(`^[0-9a-fA-F]{8,40}$`)

func (s *Server) toolMergePull(ctx context.Context, args map[string]any) (string, error) {
	r, err := s.resolveIssueRepo(args, true)
	if err != nil {
		return "", err
	}
	number := argInt(args, "number", 0)
	if number <= 0 {
		return "", errors.New("number 必须是正整数")
	}
	sha := strings.ToLower(strings.TrimSpace(argStr(args, "sha")))
	if sha == "" {
		return "", errors.New("sha 必填：先 get_pull 拿到 head_sha，确保合并的是你审查过的那次提交")
	}
	if !reHexSHA.MatchString(sha) {
		return "", fmt.Errorf("sha 不合法：%q 应是 8–40 位的十六进制 commit hash（get_pull 返回 head_sha）", sha)
	}

	// 合并前再拉一次 PR：校验 open、状态，且 sha 与当前 head 精确匹配（短 sha 用 HasPrefix）。
	p, err := s.gh.GetPull(ctx, r, number)
	if err != nil {
		return "", err
	}
	if p.State != "open" {
		return "", fmt.Errorf("PR #%d 当前是 %s 状态，不能合并（仅 open 可合并）", number, p.State)
	}
	if !strings.HasPrefix(p.HeadSHA, sha) {
		return "", fmt.Errorf("sha 不匹配：传入 %q，但当前 head_sha=%s。PR 中间可能被推进了，请重新 get_pull 后用最新 head_sha 合并",
			sha, p.HeadSHA)
	}

	title := strings.TrimSpace(argStr(args, "commit_title"))
	bodyText := strings.TrimSpace(argStr(args, "commit_body"))
	if n := utf8.RuneCountInString(title); title != "" && (n < pullMsgMinRunes || n > pullMsgMaxRunes) {
		return "", fmt.Errorf("commit_title 长度需在 %d–%d 字，当前 %d 字", pullMsgMinRunes, pullMsgMaxRunes, n)
	}
	if n := utf8.RuneCountInString(bodyText); bodyText != "" && (n < pullMsgMinRunes || n > pullMsgMaxRunes) {
		return "", fmt.Errorf("commit_body 长度需在 %d–%d 字，当前 %d 字", pullMsgMinRunes, pullMsgMaxRunes, n)
	}
	if title == "" {
		title = fmt.Sprintf("Merge PR #%d: %s", number, p.Title)
	}
	msg := title
	if bodyText != "" {
		msg += "\n\n" + bodyText
	}
	// 附代提交溯源信息：squash 合并后 commit 会包含是谁、用的什么工具合的。
	msg += "\n\n---\nSquash-merged by repoMcp on behalf of SantaChains MCP user\n" +
		"Source PR #" + itoa(number) + " merged commit " + shortSHA(p.HeadSHA)

	res, err := s.gh.MergePull(ctx, r, number, p.HeadSHA, msg)
	if err != nil {
		// 合并错误可能是 CI 未通过 / merge conflict / base 保护，把原始消息直接给出。
		return "", fmt.Errorf("合并 #%d 失败：%w", number, err)
	}
	if !res.Merged {
		extra := ""
		if res.Message != "" {
			extra = " GitHub 提示：" + res.Message
		}
		return "", fmt.Errorf("PR #%d 合并未成功，但 HTTP 返回 2xx。通常是 merge 接口竞态。稍后 get_pull 看状态%s", number, extra)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "PR #%d 已以 squash 合并完成\n", number)
	fmt.Fprintf(&sb, "分支：%s → %s\n", p.HeadRef, p.BaseRef)
	fmt.Fprintf(&sb, "生成提交：%s\n", res.SHA)
	if r.WebBase != "" && res.SHA != "" {
		fmt.Fprintf(&sb, "链接：%s/commit/%s\n", r.WebBase, res.SHA)
	}
	if res.Message != "" {
		sb.WriteString("提示：" + res.Message + "\n")
	}
	// 顺带睡眠 1s 让 GitHub 刷新完，这样用户下一次 get_pull（常用的下一步）不会还看到 open。
	time.Sleep(time.Second)
	return sb.String(), nil
}
