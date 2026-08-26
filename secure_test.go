package main

import (
	"strings"
	"testing"
)

// TestSanitizeError_GithubPATs 各类 GitHub 令牌形态的脱敏。
func TestSanitizeError_GithubPATs(t *testing.T) {
	// 各 token 严格按真实长度构造：ghp_ + 36 字符；github_pat_ + 82 字符；gho_/ghs_/ghu_ + 36 字符。
	pat36 := "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789" // 36 字符：A-Z(26) + 0-9(10) = 36
	_ = pat36
	prefixGHP := "ghp_" + pat36 // ghp_ + 36
	// github_pat_ 前缀 + 82 字符：这里用 82 字符生成一段固定文本。
	pat82 := strings.Repeat("A", 82)
	prefixGHPFine := "github_pat_" + pat82
	prefixGHO := "gho_" + pat36
	prefixGHS := "ghs_" + strings.Repeat("z", 36)

	cases := map[string]string{
		"prefix " + prefixGHP + " suffix":                 "prefix <REDACTED> suffix",
		"prefix " + prefixGHPFine + " suffix":             "prefix <REDACTED> suffix",
		"prefix " + prefixGHO + " suffix":                 "prefix <REDACTED> suffix",
		"prefix " + prefixGHS + " suffix":                 "prefix <REDACTED> suffix",
		"Authorization: " + prefixGHP + " done":          "<REDACTED> done",
		"curl with Bearer " + prefixGHP + " ok":           "curl with <REDACTED> ok",
	}
	for in, want := range cases {
		got := SanitizeError(in, nil)
		if got != want {
			t.Errorf("SanitizeError 失败\n  in  = %s\n  want= %s\n  got = %s", in, want, got)
		}
		// 确保原 token 格式不再出现在输出里。
		if strings.Contains(got, "ghp_") || strings.Contains(got, "github_pat_") ||
			strings.Contains(got, "gho_") || strings.Contains(got, "ghs_") {
			t.Errorf("输出中仍存在 token 前缀: %s", got)
		}
	}
}

// TestSanitizeError_ExtraPats 用户自定义 token。
func TestSanitizeError_ExtraPats(t *testing.T) {
	custom := "my-super-long-secret-1234567890"
	msg := "错误：连接仓库凭据 " + custom + " 已失效，请替换"
	got := SanitizeError(msg, []string{custom, "", "short"})
	if !strings.Contains(got, "<REDACTED>") {
		t.Fatalf("未发生替换 got=%s", got)
	}
	if strings.Contains(got, custom) {
		t.Fatalf("原始 custom 仍出现在输出 got=%s", got)
	}
	// 空 extraPats 被跳过。
	got2 := SanitizeError(msg, []string{"", " ", "\t"})
	if !strings.Contains(got2, custom) {
		t.Fatalf("空/空白 extraPat 不应触发替换 got=%s", got2)
	}
}

// TestSanitizeError_ExtraPatsLongFirst 长 extra 先替换，避免短先。
func TestSanitizeError_ExtraPatsLongFirst(t *testing.T) {
	// 若先替 "abc"，则 "abcd" 不完整；我们要求按长度降序，保证长优先。
	msg := "value is abcd and abc"
	got := SanitizeError(msg, []string{"abc", "abcd"})
	want := "value is <REDACTED> and <REDACTED>"
	if got != want {
		t.Fatalf("长优先失败 want=%s got=%s", want, got)
	}
}

// TestMaskShort 各种边界情况。
func TestMaskShort(t *testing.T) {
	cases := []struct {
		in        string
		head, tail int
		wantHead  byte
		wantTail  byte
		minStars  int
	}{
		{"0123456789", 4, 4, '0', '9', 2},            // 正好 head+tail。
		{"abcdefghij", 2, 2, 'a', 'j', 4},            // 6 字符中间，至少 4。
		{"a", 4, 4, '*', '*', 8},                     // 过短，*8。
		{"", 4, 4, '*', '*', 8},                      // 空。
		{"abcdefghijklmnopqrstuvwxyz0123456789", 4, 4, 'a', '9', 1}, // 长。
	}
	for i, c := range cases {
		got := maskShort(c.in, c.head, c.tail)
		if len(got) == 0 {
			t.Fatalf("[%d] 空输出", i)
		}
		stars := strings.Count(got, "*")
		if stars < c.minStars {
			t.Errorf("[%d] star 不足 min=%d got=%d result=%q", i, c.minStars, stars, got)
		}
		if got[0] != c.wantHead {
			t.Errorf("[%d] head 不匹配 want=%c got=%c result=%q", i, c.wantHead, got[0], got)
		}
		if got[len(got)-1] != c.wantTail {
			t.Errorf("[%d] tail 不匹配 want=%c got=%c result=%q", i, c.wantTail, got[len(got)-1], got)
		}
	}
}
