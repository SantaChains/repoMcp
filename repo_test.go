package main

import (
	"strings"
	"testing"
)

func TestIsProbablyBinary(t *testing.T) {
	cases := []struct {
		in   []byte
		want bool
	}{
		{nil, false},
		{[]byte{}, false},
		{[]byte("hello world\n"), false},
		{[]byte("abc\x00def"), true},
		{[]byte("\xff\xfe\xfd"), true},
		{[]byte(strings.Repeat("a", 1000) + "\xff"), false},
		{[]byte("你好，世界\n"), false},
	}
	for _, c := range cases {
		if got := IsProbablyBinary(c.in); got != c.want {
			t.Errorf("IsProbablyBinary(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSafeRelPath(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{"a/b/c.go", true, "a/b/c.go"},
		{"", false, ""},
		{"/abs/path", false, ""},
		{"a/../b", false, ""},
		{"./a", false, ""},
		{"C:/windows/x", false, ""},
		{"a//b", true, "a//b"},
		{" a.go ", true, "a.go"},
	}
	for _, c := range cases {
		got, ok := stSafeRelPath(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("stSafeRelPath(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIsHardExcluded(t *testing.T) {
	cases := []struct{ in string; want bool }{
		{"src/x.go", false},
		{"src/node_modules/lib/index.js", true},
		{"vendor/glide.lock", true},
		{"images/logo.png", true},
		{"bin/app.exe", true},
		{"package-lock.json", true},
		{"src/.git/config", true},
		{"README.md", false},
	}
	for _, c := range cases {
		if got := stIsHardExcluded(c.in); got != c.want {
			t.Errorf("stIsHardExcluded(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseNameOnly(t *testing.T) {
	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	out := shaA + "\nsrc/a.go\nb.go\n\n" + shaB + "\nc.rs\n"
	got := stParseNameOnly(out)
	if len(got) != 2 {
		t.Fatalf("块数 %d，want 2", len(got))
	}
	if files := got[shaA]; len(files) != 2 || files[0] != "src/a.go" || files[1] != "b.go" {
		t.Errorf("第一块解析错误: %v", files)
	}
	if files := got[shaB]; len(files) != 1 || files[0] != "c.rs" {
		t.Errorf("第二块解析错误: %v", files)
	}
	if got := stParseNameOnly(""); len(got) != 0 {
		t.Error("空输入应返回空 map")
	}
}

func TestParsePorcelainBlame(t *testing.T) {
	sha := strings.Repeat("1", 40)
	out := sha + " 1 1 1\n" +
		"author Alice\n" +
		"author-mail <a@x.com>\n" +
		"author-time 1609459200\n" +
		"author-tz +0000\n" +
		"committer Bob\n" +
		"committer-time 1609459200\n" +
		"summary fix bug\n" +
		"filename a.go\n" +
		"\tline one\n" +
		sha + " 2 2\n" +
		"author Alice\n" +
		"author-time 1609459200\n" +
		"\tline two\n"
	got := stParsePorcelainBlame(out)
	if len(got) != 2 {
		t.Fatalf("行数 %d，want 2", len(got))
	}
	if got[0].Line != 1 || got[0].SHA != sha || got[0].Author != "Alice" || got[0].Text != "line one" {
		t.Errorf("第一行解析错误: %+v", got[0])
	}
	if got[0].Date != "2021-01-01" {
		t.Errorf("日期 = %q, want 2021-01-01", got[0].Date)
	}
	if got[1].Line != 2 || got[1].Text != "line two" {
		t.Errorf("第二行解析错误: %+v", got[1])
	}
}

func TestLooksLikeBlameHeader(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("1", 40) + " 1 2", true},
		{strings.Repeat("1", 38) + " 1 2", false},
		{strings.Repeat("z", 40) + " 1 2", false},
		{"", false},
	}
	for _, c := range cases {
		if got := stLooksLikeBlameHeader(c.in); got != c.want {
			t.Errorf("stLooksLikeBlameHeader(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
