package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"testing"
	"time"
)

// TestWithTraceID_RoundTrip 验证 WithTraceID -> TraceID 一致性。
func TestWithTraceID_RoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "abcdef12")
	got := TraceID(ctx)
	if got != "abcdef12" {
		t.Fatalf("TraceID 往返不一致 want=abcdef12 got=%s", got)
	}
}

// TestTraceID_DefaultGenerate 不传 id 时自动生成 8 字符 hex。
func TestTraceID_DefaultGenerate(t *testing.T) {
	ctx := WithTraceID(context.Background(), "")
	got := TraceID(ctx)
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	if !re.MatchString(got) {
		t.Fatalf("自动生成 trace-id 格式错误 got=%s", got)
	}
}

// TestTraceID_NilContext 安全返回 "-"。
func TestTraceID_NilContext(t *testing.T) {
	if got := TraceID(nil); got != "-" {
		t.Fatalf("nil ctx TraceID want=- got=%s", got)
	}
}

// TestLogger_JSONValid 验证单行 JSON 合法且字段完整。
func TestLogger_JSONValid(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(&buf, LevelInfo, "ut")
	lg.InfoCtx(WithTraceID(context.Background(), "abcd1234"), "hi", "count", 3, "elapsed_ms", 1500*time.Millisecond, "err", nil)
	line := buf.Bytes()
	if len(line) == 0 || line[len(line)-1] != '\n' {
		t.Fatalf("日志必须单行换行结尾 got=%q", line)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("JSON 解析失败 err=%v line=%s", err, line)
	}
	for _, k := range []string{"ts", "level", "trace", "module", "msg", "count", "elapsed_ms"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺字段 %s 实际=%v", k, m)
		}
	}
	if m["trace"] != "abcd1234" {
		t.Fatalf("trace 错误 got=%v", m["trace"])
	}
	if m["elapsed_ms"] != float64(1500) {
		t.Fatalf("elapsed_ms 转换错误 got=%v", m["elapsed_ms"])
	}
	if m["level"] != "info" {
		t.Fatalf("level 错误 got=%v", m["level"])
	}
	if m["msg"] != "hi" {
		t.Fatalf("msg 错误 got=%v", m["msg"])
	}
}

// TestLogger_LevelFilter debug 级别在 info 过滤器下不输出。
func TestLogger_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(&buf, LevelInfo, "ut")
	lg.DebugCtx(context.Background(), "should skip")
	lg.InfoCtx(context.Background(), "should appear")
	all := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("should skip")) {
		t.Fatalf("debug 级别应该被过滤掉 got=%s", all)
	}
	if !bytes.Contains(buf.Bytes(), []byte("should appear")) {
		t.Fatalf("info 级别应该输出 got=%s", all)
	}
}

// TestLogger_ConcurrentSafety 100 并发写入 buf 不 panic（go test -race 下检测数据竞争）。
func TestLogger_ConcurrentSafety(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(&buf, LevelInfo, "ut")
	var wg sync.WaitGroup
	N := 120
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			ctx := WithTraceID(context.Background(), "")
			lg.InfoCtx(ctx, "pulse", "i", i, "elapsed_ms", time.Duration(i)*time.Millisecond)
			if i%7 == 0 {
				lg.WarnCtx(ctx, "warm pulse", "i", i)
			}
			if i%13 == 0 {
				lg.ErrorCtx(ctx, "error pulse", "i", i, "error", context.DeadlineExceeded)
			}
		}(i)
	}
	wg.Wait()
	// 解析全部行均为合法 JSON。
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) < N {
		t.Fatalf("至少应写出 N 行 got=%d", len(lines))
	}
	for idx, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal(ln, &m); err != nil {
			t.Fatalf("第 %d 行 JSON 非法 err=%v line=%s", idx, err, ln)
		}
		if _, ok := m["ts"]; !ok {
			t.Fatalf("第 %d 行缺 ts", idx)
		}
	}
}

// TestParseLevel 覆盖全部合法级别 + 大小写混合 + 未知。
func TestParseLevel(t *testing.T) {
	cases := map[string]LogLevel{
		"debug":   LevelDebug,
		"DEBUG":   LevelDebug,
		"info":    LevelInfo,
		"Info":    LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"ERROR":   LevelError,
		"foo":     LevelInfo, // 未知回退 info
		"":        LevelInfo,
	}
	for input, want := range cases {
		if got := ParseLevel(input); got != want {
			t.Errorf("ParseLevel(%q) want=%v got=%v", input, want, got)
		}
	}
}

// TestSetLevel 动态切换级别。
func TestSetLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := NewLogger(&buf, LevelInfo, "ut")
	lg.DebugCtx(context.Background(), "skipped 1")
	lg.SetLevel(LevelDebug)
	lg.DebugCtx(context.Background(), "shown 1")
	s := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("skipped 1")) {
		t.Fatalf("级别切换前 debug 不该输出 got=%s", s)
	}
	if !bytes.Contains(buf.Bytes(), []byte("shown 1")) {
		t.Fatalf("级别切到 debug 后 debug 应输出 got=%s", s)
	}
}

// TestLogLevel_String 覆盖全部级别名称。
func TestLogLevel_String(t *testing.T) {
	if LevelDebug.String() != "debug" || LevelInfo.String() != "info" ||
		LevelWarn.String() != "warn" || LevelError.String() != "error" {
		t.Fatalf("LogLevel String() 不匹配")
	}
}
