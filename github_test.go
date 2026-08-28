package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestNormState(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "open"},
		{"open", "open"},
		{"OPEN", "open"},
		{"closed", "closed"},
		{"all", "all"},
		{"any", "all"},
		{"garbage", "open"},
		{"  closed  ", "closed"},
	}
	for _, c := range cases {
		if got := ghNormState(c.in); got != c.want {
			t.Errorf("ghNormState(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTextTokens(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]bool
	}{
		{"下载 断点续传", map[string]bool{"下载": true, "断点": true, "点续": true, "续传": true}},
		{"aria2 断点续传", map[string]bool{"aria2": true, "aria": true, "断点": true, "点续": true, "续传": true}},
		{"bug 问题", map[string]bool{}},
	}
	for _, c := range cases {
		if got := ghTextTokens(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ghTextTokens(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsCJK(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
	}{
		{'中', true},
		{'a', false},
		{'が', true},
		{'한', true},
		{' ', false},
	}
	for _, c := range cases {
		if got := ghIsCJK(c.r); got != c.want {
			t.Errorf("ghIsCJK(%q) = %v, want %v", c.r, got, c.want)
		}
	}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b map[string]bool
		want float64
	}{
		{map[string]bool{"x": true, "y": true}, map[string]bool{"x": true, "y": true, "z": true}, 1.0},
		{map[string]bool{"x": true}, map[string]bool{"y": true}, 0.0},
		{map[string]bool{"x": true, "y": true}, map[string]bool{"x": true}, 1.0},
		{map[string]bool{}, map[string]bool{"x": true}, 0.0},
		{map[string]bool{"x": true, "y": true}, map[string]bool{"x": true, "z": true}, 0.5},
	}
	for _, c := range cases {
		if got := ghSimilarity(c.a, c.b); got != c.want {
			t.Errorf("ghSimilarity(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestRankByText(t *testing.T) {
	items := []Issue{
		{Number: 1, Title: "download timeout when network slow"},
		{Number: 2, Title: "add retry backoff for uploads"},
		{Number: 3, Title: "unrelated topic"},
	}
	got := ghRankByText(items, "retry backoff upload", 10)
	if len(got) == 0 || got[0].Number != 2 {
		t.Fatalf("应把最相关排前面: %+v", got)
	}
	if len(got) > 2 {
		t.Fatalf("零重合应被丢弃: %+v", got)
	}
	got2 := ghRankByText(items, "download timeout", 1)
	if len(got2) != 1 || got2[0].Number != 1 {
		t.Errorf("limit 失效: %+v", got2)
	}
}

func TestGHError(t *testing.T) {
	mkResp := func(status int, hdr http.Header) *http.Response {
		return &http.Response{StatusCode: status, Header: hdr}
	}
	r := &Repo{Slug: "o/r"}

	if err := ghError(mkResp(404, http.Header{}), []byte(`{"message":"Not Found"}`), r); !errors.Is(err, errGHNotFound) {
		t.Errorf("404 应映射到 errGHNotFound，got %v", err)
	}
	if err := ghError(mkResp(401, http.Header{}), []byte(`{"message":"Bad credentials"}`), r); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("401 报错应含状态码: %v", err)
	}
	if err := ghError(mkResp(422, http.Header{}), []byte(`{"message":"Validation Failed","errors":[{"field":"title","code":"missing"}]}`), r); err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("422 报错应含状态码: %v", err)
	}
	rl := http.Header{}
	rl.Set("X-RateLimit-Remaining", "0")
	rl.Set("X-RateLimit-Reset", "0")
	if err := ghError(mkResp(403, rl), []byte(`{}`), r); err == nil || !strings.Contains(err.Error(), "限流") {
		t.Errorf("403 限流报错应提示限流: %v", err)
	}

	// ── 403 分类细分 ──
	hdrOK := http.Header{} // remaining > 0，不是 primary 限流
	hdrOK.Set("X-RateLimit-Remaining", "10")
	cases := []struct {
		name    string
		body    string
		wantKey string // 期望错误文本包含
	}{
		{"secondary", `{"message":"You have exceeded a secondary rate limit"}`, "次级限流"},
		{"abuse", `{"message":"Maximum number of requests exceeded due to abuse detection"}`, "次级限流"},
		{"missing_scope", `{"message":"Insufficient OAuth scope to perform this action","errors":[{"code":"missing_scope"}]}`, "scope"},
		{"collab_access", `{"message":"Resource not accessible by integration"}`, "无权访问"},
		{"collaborator_check", `{"message":"You must be a collaborator to do this"}`, "无权访问"},
		{"default_403", `{"message":"Some other 403 reason"}`, "操作被拒绝"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ghError(mkResp(403, hdrOK), []byte(c.body), r)
			if err == nil {
				t.Fatal("预期返回错误，got nil")
			}
			if !strings.Contains(err.Error(), c.wantKey) {
				t.Errorf("错误应包含 %q，got: %v", c.wantKey, err)
			}
			// 关键：不再误导为"缺 issues:write"
			if strings.Contains(err.Error(), "issues:write") {
				t.Errorf("403 %s 不应硬编码 issues:write 提示，got: %v", c.name, err)
			}
		})
	}
}

func TestToIssue(t *testing.T) {
	raw := `{"number":7,"title":"  t  ","state":"open","state_reason":"completed","body":"a\r\nb","html_url":"https://x","comments":2,"labels":[{"name":"bug"},{"name":""}],"user":{"login":"alice"}}`
	var j ghIssueJSON
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		t.Fatal(err)
	}
	iss := j.toIssue()
	if iss.Number != 7 || iss.Title != "t" || iss.State != "open" || iss.Reason != "completed" {
		t.Errorf("基础字段错误: %+v", iss)
	}
	if iss.Body != "a\nb" {
		t.Errorf("CRLF 未规整: %q", iss.Body)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "bug" {
		t.Errorf("Labels = %v", iss.Labels)
	}
}
