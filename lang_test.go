package main

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"", "anything", true},
		{"*.go", "c.go", true},
		{"*.go", "a/b/c.go", true},
		{"*.go", "a/b/c.txt", false},
		{"a/*.go", "a/x.go", true},
		{"a/*.go", "b/x.go", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x", false},
		{"**/*.go", "x/y/z.go", true},
		{"**/*.go", "z.go", true},
		{"src/**", "src", true},
		{"src/**", "src/a/b", true},
		{"src/**", "other/a", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		{"a?b", "axb", true},
		{"a?b", "ab", false},
		{"a/b", "a/b", true},
		{"a/b", "a", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestSingleSegMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"", "", true},
		{"", "x", false},
		{"a", "a", true},
		{"a", "b", false},
		{"a*", "ab", true},
		{"a*b", "ab", true},
		{"a*b", "axxb", true},
		{"a*b", "a", false},
		{"*", "", true},
		{"*", "anything", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "axbyc", true},
		{"a*b*c", "ac", false},
	}
	for _, c := range cases {
		if got := ixSingleSegMatch(c.pat, c.s); got != c.want {
			t.Errorf("ixSingleSegMatch(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestDetectLang(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"main.go", "go"},
		{"a/b/c.rs", "rust"},
		{"src/MAIN.GO", "go"},
		{"Dockerfile", "dockerfile"},
		{"Makefile", "make"},
		{"foo.mk", "make"},
		{"x.test.ts", "typescript"},
		{"a.tsx", "tsx"},
		{"b.js", "javascript"},
		{"c.py", "python"},
		{"d.java", "java"},
		{"e.cpp", "cpp"},
		{"f.cs", "csharp"},
		{"g.rb", "ruby"},
		{"h.php", "php"},
		{"i.swift", "swift"},
		{"j.sql", "sql"},
		{"k.proto", "proto"},
		{"README.md", "markdown"},
		{"go.mod", "go"},
		{"sh/p.sh", "shell"},
		{".gitignore", ""},
		{"noext", ""},
		{"foo.xyz", ""},
	}
	for _, c := range cases {
		if got := DetectLang(c.path); got != c.want {
			t.Errorf("DetectLang(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestBasenameAndExt(t *testing.T) {
	cases := []struct {
		in, base, ext string
	}{
		{"a/b/c.go", "c.go", ".go"},
		{"plain", "plain", ""},
		{"", "", ""},
		{"a.b.c", "a.b.c", ".c"},
		{".gitignore", ".gitignore", ""},
	}
	for _, c := range cases {
		if got := ixBasename(c.in); got != c.base {
			t.Errorf("ixBasename(%q) = %q, want %q", c.in, got, c.base)
		}
		if got := ixExtLower(c.base); got != c.ext {
			t.Errorf("ixExtLower(%q) = %q, want %q", c.base, got, c.ext)
		}
	}
}
