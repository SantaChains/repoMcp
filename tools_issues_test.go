package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssueRateLimiter(t *testing.T) {
	l := newIssueRateLimiter(0)
	if ok, _ := l.take("r", ""); !ok {
		t.Error("不限额应放行")
	}
	if rem := l.remaining("r"); rem != -1 {
		t.Errorf("remaining = %d, want -1", rem)
	}

	l2 := newIssueRateLimiter(2)
	for i := 0; i < 2; i++ {
		if ok, _ := l2.take("r", "alice"); !ok {
			t.Error("前 2 次应放行")
		}
	}
	if rem := l2.remaining("r"); rem != 0 {
		t.Errorf("remaining = %d, want 0", rem)
	}
	if ok, wait := l2.take("r", "alice"); ok || wait <= 0 {
		t.Errorf("第 3 次应拒绝并给出等待时长: ok=%v wait=%v", ok, wait)
	}

	l3 := newIssueRateLimiter(1)
	l3.hist["r"] = []time.Time{time.Now().Add(-2 * time.Hour)}
	if ok, _ := l3.take("r", "bob"); !ok {
		t.Error("过期条目应被清理后放行")
	}
	if rem := l3.remaining("r"); rem != 0 {
		t.Errorf("清理后 remaining = %d, want 0", rem)
	}

	// reporter 桶限制：单 reporter 10 次/小时。
	l4 := newIssueRateLimiter(100)
	count := 0
	for i := 0; i < 20; i++ {
		if ok, _ := l4.take("x", "carol"); ok {
			count++
		}
	}
	if count != issueReporterPerHour {
		t.Errorf("reporter 桶限制应为 %d，实际放行 %d", issueReporterPerHour, count)
	}

	// 全局桶 flood 保护：不同 reporter 也最多 issueGlobalPerHourSoft
	l5 := newIssueRateLimiter(1000)
	global := 0
	total := 0
	for i := 0; i < issueGlobalPerHourSoft+10; i++ {
		rname := fmt.Sprintf("user_%d", i)
		total++
		if ok, _ := l5.take("any", rname); ok {
			global++
		}
	}
	if global != issueGlobalPerHourSoft {
		t.Errorf("全局桶应放行 %d，实际 %d（尝试 %d）", issueGlobalPerHourSoft, global, total)
	}
}

func TestSplitList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a,b，c\nd;e", []string{"a", "b", "c", "d", "e"}},
		{"a, A, b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"bug;enhancement", []string{"bug", "enhancement"}},
	}
	for _, c := range cases {
		if got := splitList(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIssueStateText(t *testing.T) {
	cases := []struct {
		iss  Issue
		want string
	}{
		{Issue{State: "open"}, "open"},
		{Issue{State: "closed", Reason: "completed"}, "closed/已解决"},
		{Issue{State: "closed", Reason: "not_planned"}, "closed/不予处理"},
		{Issue{State: "closed"}, "closed"},
	}
	for _, c := range cases {
		if got := issueStateText(c.iss); got != c.want {
			t.Errorf("issueStateText(%+v) = %q, want %q", c.iss, got, c.want)
		}
	}
}

func TestIssueMetaLine(t *testing.T) {
	it := Issue{Labels: []string{"bug"}, Comments: 3, CreatedAt: "2026-08-01", UpdatedAt: "2026-08-02", Author: "alice"}
	got := issueMetaLine(it)
	for _, part := range []string{"标签 bug", "3 评论", "创建 2026-08-01", "更新 2026-08-02", "作者 alice"} {
		if !strings.Contains(got, part) {
			t.Errorf("issueMetaLine 缺 %q: %q", part, got)
		}
	}
}

func TestIssueSummary(t *testing.T) {
	body := "# 标题\n\n正文第一行\n\n> 引用\n\n---\n\n正文第二行\n\n\n正文第三行\n"
	got := issueSummary(body, 2, 200)
	want := []string{"正文第一行", "正文第二行"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("issueSummary = %v, want %v", got, want)
	}
}

func TestIssueModeAndList(t *testing.T) {
	if issueMode(&Repo{}) != "off" {
		t.Error("默认 off")
	}
	if issueMode(&Repo{IssueRead: true}) != "read" {
		t.Error("read")
	}
	if issueMode(&Repo{IssueRead: true, IssueWrite: true}) != "write" {
		t.Error("write")
	}
	rs := []*Repo{{Name: "a"}, {Name: "b"}}
	if got := issueRepoList(rs); got != "a / b" {
		t.Errorf("issueRepoList = %q", got)
	}
	if got := issueRepoList(nil); got != "（无）" {
		t.Errorf("issueRepoList(nil) = %q", got)
	}
}

func TestPickLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/labels" {
			_, _ = w.Write([]byte(`[{"name":"bug"},{"name":"enhancement"},{"name":"性能"}]`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	s := &Server{gh: NewGitHub(srv.URL, time.Second)}

	// 无白名单时取仓库现有标签（大小写保真）。
	r := &Repo{Slug: "o/r"}
	keep, dropped := s.pickLabels(context.Background(), r, []string{"性能", "magic"})
	if len(keep) != 1 || keep[0] != "性能" {
		t.Errorf("keep = %v", keep)
	}
	if len(dropped) != 1 || dropped[0] != "magic" {
		t.Errorf("dropped = %v", dropped)
	}

	// 配置白名单时不访问 GitHub。
	r2 := &Repo{Slug: "o/r", IssueLabels: []string{"bug"}}
	keep, dropped = s.pickLabels(context.Background(), r2, []string{"bug", "nope"})
	if len(keep) != 1 || keep[0] != "bug" || len(dropped) != 1 {
		t.Errorf("白名单路径错误: keep=%v dropped=%v", keep, dropped)
	}
}

func TestRenderComment(t *testing.T) {
	got := (&Server{}).renderComment("结论")
	if !strings.Contains(got, "结论") || !strings.Contains(got, "repoMcp") {
		t.Errorf("renderComment = %q", got)
	}
}

func TestRenderIssueBody(t *testing.T) {
	// Dir 指向不存在的路径，避免 shortHead 在测试进程 cwd 里跑 git。
	dir := t.TempDir() + "/nonexistent"
	s := &Server{store: NewStore([]*Repo{{Name: "r", Dir: dir}})}
	body := s.renderIssueBody(&Repo{Name: "r"}, "用户报错：下载中断", "已定位 src/download.go:42", true, "步骤", "Windows 11", "张三")
	for _, part := range []string{"用户报错：下载中断", "已在源码中定位", "src/download.go:42", "复现 / 触发条件", "环境", "张三"} {
		if !strings.Contains(body, part) {
			t.Errorf("正文缺 %q:\n%s", part, body)
		}
	}
	b2 := s.renderIssueBody(&Repo{Name: "r"}, "x", "检索未命中", false, "", "", "")
	if !strings.Contains(b2, "未能在源码中确认") {
		t.Errorf("unconfirmed 标注缺失:\n%s", b2)
	}
}
