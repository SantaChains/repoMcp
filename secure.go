package main

import (
	"os"
	"regexp"
	"runtime"
	"strings"
)

// 预编译的敏感信息正则，按"从长匹配到短匹配"的顺序依次执行，避免互吞。
var (
	reGithubPATClassic   = regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)
	reGithubPATFineGrain = regexp.MustCompile(`github_pat_[A-Za-z0-9_]{82}`)
	reGithubOAuth        = regexp.MustCompile(`gho_[A-Za-z0-9]{36}`)
	reGithubAppInstall   = regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`)
	reGithubAppUser      = regexp.MustCompile(`ghu_[A-Za-z0-9]{36}`)
	reBearerHeader       = regexp.MustCompile(`(?i)Bearer\s+[^\s\"\']+`)
	reAuthzHeader        = regexp.MustCompile(`(?i)Authorization:\s*[^\s\"\']+`)
)

// CheckFilePermission 检查 cfg 配置文件所在路径的权限位；
// 仅在非 Windows 平台做 0600 严格权限（group/other 不可读写）检查。
// Windows 使用 ACL 模型，权限位检查无意义，仅返回不做动作。
//
// logWarn 用于打印告警；由调用方传入以避免循环依赖。
func CheckFilePermission(cfgPath string, logWarn func(format string, args ...any)) {
	if logWarn == nil || cfgPath == "" {
		return
	}
	if runtime.GOOS == "windows" {
		return
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		return
	}
	mode := st.Mode().Perm()
	// group/other 读写位：0o077。
	if mode&0o077 != 0 {
		logWarn("配置文件 %s 权限过于开放（%04o，建议 0600），建议执行 chmod 0600 %s",
			cfgPath, uint32(mode), cfgPath)
	}
}

// SanitizeError 清除 msg 中的已知 Token 模式与 extraPats（如用户自定义 token、本地路径前缀
// 等）中出现的子串，统一替换为 <REDACTED>。
//
// 执行顺序：先用正则（长模式优先）替换标准 GitHub 与 HTTP 头，再对 extraPats 中非空
// 条目做精确 Contains 替换。替换过程保证从长到短，避免短的 extra 先吞掉长的。
func SanitizeError(msg string, extraPats []string) string {
	out := msg
	regs := []*regexp.Regexp{
		reGithubPATFineGrain,
		reGithubPATClassic,
		reGithubAppInstall,
		reGithubAppUser,
		reGithubOAuth,
		reAuthzHeader,
		reBearerHeader,
	}
	for _, re := range regs {
		out = re.ReplaceAllString(out, "<REDACTED>")
	}
	// extraPats 去重 + 按长度降序替换，避免先短后长导致剩余字符无法匹配。
	seen := make(map[string]struct{}, len(extraPats))
	sorted := make([]string, 0, len(extraPats))
	for _, p := range extraPats {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		sorted = append(sorted, p)
	}
	// 简单插入式从长到短排序（extraPats 通常 < 20）。
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && len(sorted[j]) > len(sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	for _, p := range sorted {
		out = strings.ReplaceAll(out, p, "<REDACTED>")
	}
	return out
}

// collectSensitiveTokens 从 config 抽取所有应被视为敏感的 token，用于传给 SanitizeError。
// 返回顺序：全局 Token -> 全局 GitHubToken -> 各仓库 issues.token（非空）。
func collectSensitiveTokens(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, 2+len(cfg.Repos))
	if cfg.Token != "" {
		out = append(out, cfg.Token)
	}
	if cfg.GitHubToken != "" {
		out = append(out, cfg.GitHubToken)
	}
	for _, r := range cfg.Repos {
		if r.Issues != nil && strings.TrimSpace(r.Issues.Token) != "" {
			out = append(out, strings.TrimSpace(r.Issues.Token))
		}
	}
	return out
}
