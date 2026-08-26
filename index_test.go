package main

import (
	"math"
	"reflect"
	"sort"
	"testing"
)

func TestSplitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"parseHTTPResponse", []string{"parse", "HTTP", "Response"}},
		{"max_retry_count", []string{"max", "retry", "count"}},
		{"aria2StatusStr", []string{"aria", "2", "Status", "Str"}},
		{"HTTPServer", []string{"HTTP", "Server"}},
		{"my1Var", []string{"my", "1", "Var"}},
		{"ABC2def", []string{"ABC", "2def"}},
		{"URL", []string{"URL"}},
		{"url", []string{"url"}},
		{"simple", []string{"simple"}},
		{"", nil},
	}
	for _, c := range cases {
		if got := ixSplitWords(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ixSplitWords(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"parseHTTPResponse", []string{"parsehttpresponse", "parse", "http", "response"}},
		{"max_retry_count", []string{"max_retry_count", "max", "retry", "count"}},
		{"a b", []string{}},
		{"Go 语言", []string{"go"}},
		{"中文", []string{}},
		{"aria2", []string{"aria2", "aria"}},
	}
	for _, c := range cases {
		got := ixTokenize(c.in)
		sort.Strings(got)
		sort.Strings(c.want)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ixTokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"aria2StatusStr", "aria2statusstr"},
		{"aria2_status_str", "aria2statusstr"},
		{"parseHTTPResponse", "parsehttpresponse"},
		{"A_B", "ab"},
	}
	for _, c := range cases {
		if got := ixNormalizeName(c.in); got != c.want {
			t.Errorf("ixNormalizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsNoisyPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"src/foo_test.go", true},
		{"pkg/config.go", false},
		{"vendor/glide.yaml", true},
		{"generated_model.pb.go", true},
		{"main.go", false},
	}
	for _, c := range cases {
		if got := ixIsNoisyPath(c.in); got != c.want {
			t.Errorf("ixIsNoisyPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIDF(t *testing.T) {
	if got := ixIDF(0, 0); got != 0 {
		t.Errorf("ixIDF(0,0) = %v, want 0", got)
	}
	if got := ixIDF(10, 0); got != 0 {
		t.Errorf("ixIDF(10,0) = %v, want 0", got)
	}
	if got := ixIDF(10, 10); got <= 0 {
		t.Errorf("ixIDF(10,10) = %v, want > 0", got)
	}
}

func TestBM25Term(t *testing.T) {
	// tf=1 且 dl==avgdl 时 denom=tf+k1，结果恰为 1。
	if got := ixBM25Term(1, 5, 5); math.Abs(got-1) > 1e-9 {
		t.Errorf("ixBM25Term(1,5,5) = %v, want 1", got)
	}
	// avgdl<=0 按 1 处理，避免除零。
	if got := ixBM25Term(1, 0, 0); got <= 0 {
		t.Errorf("ixBM25Term(1,0,0) = %v, want > 0", got)
	}
	// 词频越高得分越高。
	if lo, hi := ixBM25Term(1, 5, 5), ixBM25Term(3, 5, 5); hi <= lo {
		t.Errorf("tf 单调性失效：%v > %v", lo, hi)
	}
}

func TestSearchCoverageGate(t *testing.T) {
	ix := NewIndex()
	ix.Replace("repoA", []File{
		{Repo: "repoA", Path: "a.go", Lang: "go", Lines: []string{
			"package main",
			"func handleRetryCount(token string) {",
			"    _ = token",
			"    return",
			"}"}},
		{Repo: "repoA", Path: "b.go", Lang: "go", Lines: []string{
			"package main",
			"const max_retry_count = 3",
		}},
	})

	// 生造词：即使 a.go 里有 token 子词也不得命中（覆盖率门槛）。
	if hits := ix.Search(SearchQuery{Text: "zzqqxx_not_exist_token", K: 8}); len(hits) != 0 {
		t.Fatalf("覆盖率门槛失效：%v", hits)
	}

	// 整词命中只应落在 b.go。
	hits := ix.Search(SearchQuery{Text: "max_retry_count", K: 8})
	if len(hits) == 0 {
		t.Fatal("整词命中无结果")
	}
	for _, h := range hits {
		if h.Path != "b.go" {
			t.Errorf("整词命中返回了错误文件 %s", h.Path)
		}
	}
}

func TestFindSymbolCrossNaming(t *testing.T) {
	ix := NewIndex()
	ix.Replace("r", []File{
		{Repo: "r", Path: "x.rs", Lang: "rust", Lines: []string{
			"fn aria2_status_str() -> i32 { 0 }",
		}},
	})
	syms := ix.FindSymbol("aria2StatusStr", "", "", 20)
	if len(syms) == 0 {
		t.Fatal("跨命名风格未命中")
	}
	if syms[0].Name != "aria2_status_str" {
		t.Errorf("命中 %q，want aria2_status_str", syms[0].Name)
	}
}

func TestIndexFileSuffix(t *testing.T) {
	ix := NewIndex()
	ix.Replace("r", []File{
		{Repo: "r", Path: "src/a/main.go", Lang: "go", Lines: []string{"a"}},
		{Repo: "r", Path: "src/b/main.go", Lang: "go", Lines: []string{"b"}},
	})
	if _, ok := ix.File("r", "main.go"); ok {
		t.Fatal("后缀不唯一不应命中")
	}
	f, ok := ix.File("r", "src/a/main.go")
	if !ok || f.Path != "src/a/main.go" {
		t.Fatalf("完整路径未命中: %v", ok)
	}
	if _, ok := ix.File("r", "a/main.go"); !ok {
		t.Fatal("唯一后缀应命中")
	}
}

func TestStatsAndTree(t *testing.T) {
	ix := NewIndex()
	ix.Replace("r", []File{
		{Repo: "r", Path: "a/b/c.go", Lang: "go", Lines: []string{"package p", "func F() {}"}},
		{Repo: "r", Path: "a/d.rs", Lang: "rust", Lines: []string{"fn main() {}"}},
	})
	st := ix.Stats()["r"]
	if st.Files != 2 || st.Lines != 3 || st.Symbols != 2 {
		t.Errorf("Stats = %+v", st)
	}
	if st.ByLang["go"] != 1 || st.ByLang["rust"] != 1 {
		t.Errorf("ByLang = %v", st.ByLang)
	}
	if tree := ix.Tree("r", 10); len(tree) == 0 {
		t.Fatal("Tree 为空")
	}
	if got := ixDirDepth("a/b/c.go", 3); got != "a/b" {
		t.Errorf("ixDirDepth = %q", got)
	}
	if got := ixDirDepth("top.go", 3); got != "." {
		t.Errorf("ixDirDepth(单文件) = %q", got)
	}
}

func TestSearchFilters(t *testing.T) {
	ix := NewIndex()
	ix.Replace("r", []File{
		{Repo: "r", Path: "a.go", Lang: "go", Lines: []string{"func main() { _ = max_retry_count }"}},
		{Repo: "r", Path: "a.rs", Lang: "rust", Lines: []string{"fn main() { let _ = max_retry_count }"}},
	})
	got := ix.Search(SearchQuery{Text: "max_retry_count", Lang: "rust", K: 8})
	if len(got) != 1 || got[0].Path != "a.rs" {
		t.Errorf("lang 过滤失败: %v", got)
	}
	got = ix.Search(SearchQuery{Text: "max_retry_count", PathGlob: "**/*.go", K: 8})
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("path_glob 过滤失败: %v", got)
	}
	got = ix.Search(SearchQuery{Text: "max_retry_count", Repo: "nope", K: 8})
	if len(got) != 0 {
		t.Errorf("未知 repo 不应有结果: %v", got)
	}
}
