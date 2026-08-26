package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDur(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 3 * time.Minute, false},
		{"0", 0, false},
		{"5m", 5 * time.Minute, false},
		{"2h30m", 2*time.Hour + 30*time.Minute, false},
		{"bogus", 0, true},
	}
	for _, c := range cases {
		got, err := parseDur(c.in, 3*time.Minute)
		if (err != nil) != c.err {
			t.Errorf("parseDur(%q) err = %v, want err=%v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("parseDur(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDeriveWebBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/a/b.git", "https://github.com/a/b"},
		{"https://user:token@github.com/a/b.git", "https://github.com/a/b"},
		{"https://github.com/a/b", "https://github.com/a/b"},
		{"git@github.com:a/b.git", "https://github.com/a/b"},
		{"git@host.example.com:group/proj.git", "https://host.example.com/group/proj"},
		{"/local/path/repo", ""},
		{"ssh://git@github.com/a/b.git", ""},
	}
	for _, c := range cases {
		if got := deriveWebBase(c.in); got != c.want {
			t.Errorf("deriveWebBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		web, url, want string
	}{
		{"https://github.com/a/b", "", "a/b"},
		{"", "git@github.com:x/y.git", "x/y"},
		{"", "https://github.com/p/q", "p/q"},
		{"https://github.com/a", "", ""},
		{"", "/local/path", ""},
	}
	for _, c := range cases {
		if got := deriveSlug(c.web, c.url); got != c.want {
			t.Errorf("deriveSlug(%q,%q) = %q, want %q", c.web, c.url, got, c.want)
		}
	}
}

func TestBuildRepos(t *testing.T) {
	mkCfg := func(repos ...RepoConfig) *Config {
		return &Config{DataDir: "data", Repos: repos}
	}

	t.Run("合法条目派生字段", func(t *testing.T) {
		cfg := mkCfg(RepoConfig{Name: "abc", URL: "https://github.com/o/r.git"})
		repos, err := cfg.BuildRepos()
		if err != nil {
			t.Fatal(err)
		}
		r := repos[0]
		if r.Name != "abc" || r.Ref != "main" || r.WebBase != "https://github.com/o/r" {
			t.Errorf("字段未正确派生: %+v", r)
		}
		abs, _ := filepath.Abs("data/abc")
		if r.Dir != abs {
			t.Errorf("Dir = %q, want %q", r.Dir, abs)
		}
	})

	t.Run("非法仓库名拒绝", func(t *testing.T) {
		if _, err := mkCfg(RepoConfig{Name: "Bad Name", URL: "u"}).BuildRepos(); err == nil {
			t.Fatal("应拒绝非法仓库名")
		}
	})

	t.Run("仓库名重复拒绝", func(t *testing.T) {
		cfg := mkCfg(
			RepoConfig{Name: "a", URL: "u1"},
			RepoConfig{Name: "A", URL: "u2"},
		)
		if _, err := cfg.BuildRepos(); err == nil || !strings.Contains(err.Error(), "重复") {
			t.Fatalf("应拒绝重复仓库名: %v", err)
		}
	})

	t.Run("URL 为空拒绝", func(t *testing.T) {
		if _, err := mkCfg(RepoConfig{Name: "a"}).BuildRepos(); err == nil {
			t.Fatal("应拒绝空 URL")
		}
	})

	t.Run("issue 推导 slug", func(t *testing.T) {
		cfg := mkCfg(RepoConfig{Name: "a", URL: "https://github.com/o/r", Issues: &RepoIssuesConfig{}})
		repos, err := cfg.BuildRepos()
		if err != nil {
			t.Fatal(err)
		}
		if repos[0].Slug != "o/r" || !repos[0].IssueRead {
			t.Errorf("slug 未推导: %+v", repos[0])
		}
	})

	t.Run("write 无令牌拒绝", func(t *testing.T) {
		cfg := mkCfg(RepoConfig{Name: "a", URL: "https://github.com/o/r", Issues: &RepoIssuesConfig{Write: true}})
		if _, err := cfg.BuildRepos(); err == nil {
			t.Fatal("write=true 无令牌应启动失败")
		}
	})

	t.Run("issue slug 推不出拒绝", func(t *testing.T) {
		cfg := mkCfg(RepoConfig{Name: "a", URL: "/local/path", Issues: &RepoIssuesConfig{}})
		if _, err := cfg.BuildRepos(); err == nil {
			t.Fatal("slug 推不出应报错")
		}
	})
}

func TestLoadConfig(t *testing.T) {
	write := func(t *testing.T, s string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	clearEnv := func(t *testing.T) {
		for _, k := range []string{"REPOMCP_CONFIG", "REPOMCP_LISTEN", "REPOMCP_TOKEN", "REPOMCP_DATA", "REPOMCP_GITHUB_TOKEN"} {
			t.Setenv(k, "")
		}
	}

	t.Run("默认值", func(t *testing.T) {
		clearEnv(t)
		cfg, err := LoadConfig(write(t, `{"repos":[{"name":"a","url":"https://github.com/o/r"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Listen != ":8790" || cfg.DataDir != "./data" || cfg.MaxResponseBytes != 12000 {
			t.Errorf("默认值不符: listen=%q dataDir=%q budget=%d", cfg.Listen, cfg.DataDir, cfg.MaxResponseBytes)
		}
		if cfg.syncInterval != 15*time.Minute || cfg.gitTimeout != 3*time.Minute || cfg.ghTimeout != 20*time.Second {
			t.Errorf("派生 duration 不符")
		}
		if cfg.issueLimit != 5 {
			t.Errorf("issueLimit = %d, want 5", cfg.issueLimit)
		}
	})

	t.Run("未知字段拒绝", func(t *testing.T) {
		clearEnv(t)
		if _, err := LoadConfig(write(t, `{"typo":1,"repos":[{"name":"a","url":"u"}]}`)); err == nil {
			t.Fatal("应拒绝未知字段")
		}
	})

	t.Run("repos 为空拒绝", func(t *testing.T) {
		clearEnv(t)
		if _, err := LoadConfig(write(t, `{"repos":[]}`)); err == nil {
			t.Fatal("应拒绝空 repos")
		}
	})

	t.Run("响应预算过小拒绝", func(t *testing.T) {
		clearEnv(t)
		if _, err := LoadConfig(write(t, `{"maxResponseBytes":500,"repos":[{"name":"a","url":"u"}]}`)); err == nil {
			t.Fatal("应拒绝过小预算")
		}
	})

	t.Run("环境变量覆盖", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("REPOMCP_LISTEN", ":9999")
		t.Setenv("REPOMCP_TOKEN", "sekret")
		t.Setenv("REPOMCP_GITHUB_TOKEN", "ghp_x")
		cfg, err := LoadConfig(write(t, `{"repos":[{"name":"a","url":"u"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Listen != ":9999" || cfg.Token != "sekret" || cfg.GitHubToken != "ghp_x" {
			t.Errorf("环境变量未覆盖: %+v", cfg)
		}
	})
}
