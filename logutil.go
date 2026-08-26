package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	mrand "math/rand"
	"strings"
	"sync"
	"time"
)

// traceIDContextKey 作为 trace-id 在 context 中的不可导出键类型，避免与其它包碰撞。
type traceIDContextKey struct{}

// traceIDContextKeyInstance 是唯一的 trace-id context 键实例。
var traceIDContextKeyInstance = traceIDContextKey{}

// defaultTraceLen 默认 trace-id 十六进制字符串长度（4字节 -> 8 字符）。
const defaultTraceLen = 8

// WithTraceID 将 trace-id 注入 ctx；若 id 为空将自动生成。返回新 ctx。
func WithTraceID(ctx context.Context, id string) context.Context {
	if id == "" {
		id = newTraceID()
	}
	return context.WithValue(ctx, traceIDContextKeyInstance, id)
}

// TraceID 从 ctx 中取出 trace-id；不存在时返回 "-"。
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return "-"
	}
	v, ok := ctx.Value(traceIDContextKeyInstance).(string)
	if !ok || v == "" {
		return "-"
	}
	return v
}

// newTraceID 生成一个 8 字符 hex；优先用 crypto/rand，失败退化为 math/rand。
func newTraceID() string {
	b := make([]byte, defaultTraceLen/2)
	_, err := rand.Read(b)
	if err != nil {
		// 退化到 math/rand（不影响功能）。
		sb := make([]byte, defaultTraceLen/2)
		mr := mrand.New(mrand.NewSource(time.Now().UnixNano()))
		_, _ = mr.Read(sb)
		b = sb
	}
	return hex.EncodeToString(b)
}

// LogLevel 日志级别。
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String 返回级别的小写名称。
func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "info"
}

// ParseLevel 从字符串解析级别；未知时回退 info。大小写不敏感。
func ParseLevel(s string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	}
	return LevelInfo
}

// Logger 是一个最小化结构化 JSON 日志器，仅依赖标准库。
//
// 输出单行 JSON：
//
//	{"ts":"2024-01-01T00:00:00.000Z","level":"info","trace":"abcd1234","module":"default","msg":"hello","k1":"v1"}
type Logger struct {
	mu     sync.Mutex
	out    io.Writer
	level  LogLevel
	module string
}

// Default 是包级默认日志实例（输出到 Stdout，级别 Info，模块 default）。
var Default = NewLogger(os.Stdout, LevelInfo, "default")

// NewLogger 创建一个新的 Logger。
func NewLogger(out io.Writer, level LogLevel, module string) *Logger {
	if out == nil {
		out = os.Stdout
	}
	if module == "" {
		module = "default"
	}
	return &Logger{out: out, level: level, module: module}
}

// SetLevel 动态切换日志级别。
func (l *Logger) SetLevel(level LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// Level 返回当前级别。
func (l *Logger) Level() LogLevel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetOutput 切换输出目标。
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w != nil {
		l.out = w
	}
}

// logKV 内部实现：按指定级别写一条 JSON 日志。
// kvs 为交替 key-value 对；若最后是单 key 则忽略。
//
// 特殊语义：
//
//	key="elapsed_ms" 且 value 为 time.Duration -> 转换为整数字节毫秒。
//	key="error" 且 value 为 error -> 转换为 err.Error() 字符串。
func (l *Logger) logKV(ctx context.Context, level LogLevel, msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	record := make(map[string]any, 4+len(kvs)/2)
	record["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	record["level"] = level.String()
	record["trace"] = TraceID(ctx)
	record["module"] = l.module
	record["msg"] = msg

	for i := 0; i+1 < len(kvs); i += 2 {
		k, ok := kvs[i].(string)
		if !ok {
			continue
		}
		v := kvs[i+1]
		switch t := v.(type) {
		case time.Duration:
			if k == "elapsed_ms" {
				record[k] = int64(t / time.Millisecond)
			} else {
				record[k] = t.String()
			}
		case error:
			record[k] = t.Error()
		case nil:
			record[k] = nil
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64, bool, string, []byte:
			record[k] = t
		default:
			// 复杂类型尝试用 %v。
			record[k] = fmt.Sprintf("%v", t)
		}
	}

	data, err := json.Marshal(record)
	if err != nil {
		// 序列化失败：退化为双引号安全的 fmt 输出，避免丢失错误。
		safeMsg := strings.ReplaceAll(
			strings.ReplaceAll(fmt.Sprintf("msg=%q kvs=%v", msg, kvs), "\\", "\\\\"),
			"\"", "\\\"")
		fmt.Fprintf(l.out, "{\"ts\":\"%s\",\"level\":\"error\",\"trace\":\"%s\",\"module\":\"%s\",\"msg\":\"marshal log failed: %s\"}\n",
			time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), TraceID(ctx), l.module, safeMsg)
		return
	}
	// 必须单行 + 换行。
	data = append(data, '\n')
	_, _ = l.out.Write(data)
}

// DebugCtx 写 debug 级别日志。
func (l *Logger) DebugCtx(ctx context.Context, msg string, kvs ...any) {
	l.logKV(ctx, LevelDebug, msg, kvs...)
}

// InfoCtx 写 info 级别日志。
func (l *Logger) InfoCtx(ctx context.Context, msg string, kvs ...any) {
	l.logKV(ctx, LevelInfo, msg, kvs...)
}

// WarnCtx 写 warn 级别日志。
func (l *Logger) WarnCtx(ctx context.Context, msg string, kvs ...any) {
	l.logKV(ctx, LevelWarn, msg, kvs...)
}

// ErrorCtx 写 error 级别日志。
func (l *Logger) ErrorCtx(ctx context.Context, msg string, kvs ...any) {
	l.logKV(ctx, LevelError, msg, kvs...)
}

// Infof 兼容老的 printf 风格 info 日志。kvs 可选追加。
func (l *Logger) Infof(format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.logKV(context.Background(), LevelInfo, msg)
}

// Warnf 同上，warn。
func (l *Logger) Warnf(format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.logKV(context.Background(), LevelWarn, msg)
}

// Errorf 同上，error。
func (l *Logger) Errorf(format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.logKV(context.Background(), LevelError, msg)
}

// ErrorfCtx 带 ctx + printf 风格的 error。
func (l *Logger) ErrorfCtx(ctx context.Context, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.logKV(ctx, LevelError, msg)
}

// InfofCtx 同上，info。
func (l *Logger) InfofCtx(ctx context.Context, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	l.logKV(ctx, LevelInfo, msg)
}

// 以下为全局便捷函数，代理到 Default。

// LogDebugCtx 代理到 Default.DebugCtx。
func LogDebugCtx(ctx context.Context, msg string, kvs ...any) { Default.DebugCtx(ctx, msg, kvs...) }

// LogInfoCtx 代理。
func LogInfoCtx(ctx context.Context, msg string, kvs ...any) { Default.InfoCtx(ctx, msg, kvs...) }

// LogWarnCtx 代理。
func LogWarnCtx(ctx context.Context, msg string, kvs ...any) { Default.WarnCtx(ctx, msg, kvs...) }

// LogErrorCtx 代理。
func LogErrorCtx(ctx context.Context, msg string, kvs ...any) { Default.ErrorCtx(ctx, msg, kvs...) }

// LogInfof 代理。
func LogInfof(format string, args ...any) { Default.Infof(format, args...) }

// LogWarnf 代理。
func LogWarnf(format string, args ...any) { Default.Warnf(format, args...) }

// LogErrorf 代理。
func LogErrorf(format string, args ...any) { Default.Errorf(format, args...) }

// elapsedMs 返回耗时的整数字节毫秒表达，用于日志字段的显式构造。
// （与 logKV 中特殊 key 的语义保持一致，便于调用方用普通 kvs 写入。）
func elapsedMs(d time.Duration) int64 {
	ms := float64(d) / float64(time.Millisecond)
	return int64(math.Round(ms))
}

// 保证 Logger 方法签名在编译期可被检测到（防止未来删除核心方法而遗漏调用方）。
// 以下为未使用的保护变量，仅用于静态验证。
var _ = (*Logger)(nil).SetLevel
var _ = (*Logger)(nil).Level
var _ = ParseLevel
