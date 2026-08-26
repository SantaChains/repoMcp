package main

import (
	"strings"
	"testing"
)

func TestExtractSymbolsGo(t *testing.T) {
	f := File{Repo: "r", Path: "x.go", Lang: "go", Lines: []string{
		"// Parse parses input.",
		"func Parse(in string) error {",
		"    return nil",
		"}",
		"func (s *Server) Serve() {",
		"}",
		"type Config struct {",
		"    Name string",
		"}",
		"type Reader interface {",
		"    Read() int",
		"}",
		"type Handler = func()",
		"const DefaultPort = 8080",
		"var version = \"1\"",
		"const (",
		"    FlagA = 1",
		"    FlagB = 2",
		")",
	}}
	syms := ExtractSymbols(f)
	want := map[string]string{
		"Parse":       "func",
		"Serve":       "method",
		"Config":      "struct",
		"Reader":      "interface",
		"Handler":     "type",
		"DefaultPort": "const",
		"version":     "var",
		"FlagA":       "const",
		"FlagB":       "const",
	}
	if len(syms) != len(want) {
		t.Fatalf("符号数 %d，want %d：%+v", len(syms), len(want), syms)
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
	for _, s := range syms {
		if s.Name == "Parse" && s.Doc != "Parse parses input." {
			t.Errorf("Parse Doc = %q", s.Doc)
		}
	}
}

func TestExtractSymbolsRust(t *testing.T) {
	f := File{Repo: "r", Path: "x.rs", Lang: "rust", Lines: []string{
		"/// Sums numbers.",
		"pub async fn sum(a: i32, b: i32) -> i32 {",
		"    a + b",
		"}",
		"/// Point docs.",
		"pub struct Point {",
		"    x: i32,",
		"}",
		"enum Color { Red, Green }",
		"pub trait Draw {",
		"    fn draw(&self);",
		"}",
		"impl Draw for Circle {",
		"    fn draw(&self) {}",
		"}",
		"impl Circle {",
		"    fn area(&self) -> f64 { 0.0 }",
		"}",
		"type Pair = (i32, i32);",
		"macro_rules! my_macro { () => {} }",
		"pub static NAME: &str = \"x\";",
		"const MAX: usize = 10;",
	}}
	syms := ExtractSymbols(f)
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	want := map[string]string{
		"sum":      "func",
		"Point":    "struct",
		"Color":    "enum",
		"Draw":     "trait",
		"Circle":   "impl",
		"Pair":     "type",
		"my_macro": "macro",
		"NAME":     "var",
		"MAX":      "const",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
	for _, s := range syms {
		if s.Name == "Point" && !strings.Contains(s.Doc, "Point") {
			t.Errorf("Point 文档未收集: %q", s.Doc)
		}
	}
}

func TestExtractSymbolsPython(t *testing.T) {
	f := File{Repo: "r", Path: "x.py", Lang: "python", Lines: []string{
		"import os",
		"",
		"class Service:",
		"    def __init__(self):",
		"        pass",
		"",
		"@decorator",
		"def parse(data):",
		"    return data",
		"",
		"async def fetch(url):",
		"    return None",
	}}
	syms := ExtractSymbols(f)
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	want := map[string]string{
		"Service":  "class",
		"__init__": "func",
		"parse":    "func",
		"fetch":    "func",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
}

func TestExtractSymbolsTS(t *testing.T) {
	f := File{Repo: "r", Path: "x.ts", Lang: "typescript", Lines: []string{
		"export function parse(input: string): number {",
		"    return 0",
		"}",
		"export class Parser {",
		"    run() {",
		"        return 1",
		"    }",
		"}",
		"export interface P {",
		"    x: number",
		"}",
		"export type Alias = number;",
		"export const handler = (e: Event) => {",
		"    return e",
		"}",
		"const BARE = (x: number) => x;",
		"export async function load() {",
		"    return null",
		"}",
	}}
	syms := ExtractSymbols(f)
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	want := map[string]string{
		"parse":   "func",
		"Parser":  "class",
		"run":     "method",
		"P":       "interface",
		"Alias":   "type",
		"handler": "func",
		"BARE":    "func",
		"load":    "func",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
}

func TestExtractSymbolsSQL(t *testing.T) {
	f := File{Repo: "r", Path: "s.sql", Lang: "sql", Lines: []string{
		"CREATE TABLE users (id INTEGER PRIMARY KEY);",
		"CREATE OR REPLACE VIEW active AS SELECT * FROM users;",
		"CREATE INDEX idx_users_name ON users(name);",
		"CREATE FUNCTION plus_one(x INT) RETURNS INT AS $$ SELECT x + 1 $$;",
	}}
	syms := ExtractSymbols(f)
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	want := map[string]string{
		"users":          "table",
		"active":         "view",
		"idx_users_name": "index",
		"plus_one":       "function",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("SQL 符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
}

func TestExtractSymbolsProto(t *testing.T) {
	f := File{Repo: "r", Path: "p.proto", Lang: "proto", Lines: []string{
		"message User {",
		"  string name = 1;",
		"}",
		"service UserService {",
		"  rpc GetUser (User) returns (User);",
		"}",
		"enum Role {",
		"  ADMIN = 0;",
		"}",
	}}
	syms := ExtractSymbols(f)
	got := map[string]string{}
	for _, s := range syms {
		got[s.Name] = s.Kind
	}
	want := map[string]string{
		"User":        "message",
		"UserService": "service",
		"GetUser":     "rpc",
		"Role":        "enum",
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("proto 符号 %s kind = %q, want %q", name, got[name], kind)
		}
	}
}

func TestExtractSymbolsUnknownLang(t *testing.T) {
	f := File{Repo: "r", Path: "x.txt", Lang: ""}
	if got := ExtractSymbols(f); got != nil {
		t.Errorf("未知语言应返回 nil，got %v", got)
	}
}
