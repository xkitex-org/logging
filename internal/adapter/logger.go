package adapter

import (
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/kitex/pkg/klog"
	"go.uber.org/zap/zapcore"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/config"
)

// Logger 在固定实例内配置输出，避免服务启动后再次替换 Kitex 全局 logger。
type Logger struct {
	mu           sync.RWMutex
	backend      *backend
	settings     config.Settings
	level        klog.Level
	configured   bool
	closed       bool
	closeErr     error
	buffering    bool
	startup      []startupEntry
	startupBytes int
}

var _ klog.FullLogger = (*Logger)(nil)

// New 创建无文件、无全局副作用的日志实现，配置成功前暂存有界启动日志。
func New() *Logger {
	cfg := config.Defaults()
	return &Logger{backend: writerBackend(cfg, os.Stderr), settings: cfg, level: klog.LevelInfo, buffering: true}
}

// ConfigureLogging 单次应用合并后的 logging 节，校验及文件打开成功后才替换默认输出。
func (l *Logger) ConfigureLogging(body []byte) error {
	cfg, err := config.Parse(body)
	if err != nil {
		l.finishStartup()
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.configured || l.closed {
		return errors.New("logging: configuration may only be applied once before close")
	}
	next, err := configuredBackend(cfg)
	if err != nil {
		l.flushStartup()
		l.buffering = false
		return err
	}
	l.closeErr = errors.Join(l.closeErr, l.backend.close())
	l.backend, l.settings, l.level, l.configured = next, cfg, kitexLevel(cfg.Level), true
	l.flushStartup()
	l.buffering = false
	return nil
}

// SetLevel 并发安全地调整 Kitex 级别，Trace 和 Notice 的过滤独立于 Zap 的级别映射。
func (l *Logger) SetLevel(level klog.Level) {
	if level < klog.LevelTrace || level > klog.LevelFatal {
		return
	}
	l.mu.Lock()
	l.level = level
	l.mu.Unlock()
}

// SetOutput 将当前输出替换为业务指定 Writer，不接管 Writer 的关闭或刷新职责。
func (l *Logger) SetOutput(writer io.Writer) {
	if writer == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeErr = errors.Join(l.closeErr, l.backend.close())
	l.backend = writerBackend(l.settings, writer)
}

// Sync 显式刷新时结束启动缓存并输出到当前后端；终端和普通 Writer 不执行文件系统 Sync。
func (l *Logger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushStartup()
	l.buffering = false
	return l.backend.core.Sync()
}

// Close 幂等刷新并关闭轮转文件，随后保留控制台输出供 Server 返回后的错误日志使用。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.flushStartup()
		l.buffering = false
		l.closeErr = errors.Join(l.closeErr, l.backend.close())
		l.backend = writerBackend(l.settings, os.Stderr)
		l.closed = true
	}
	return l.closeErr
}

// write 按 Kitex 级别过滤，经 Zap 编码输出，并将调用位置定位到业务或框架调用处。
func (l *Logger) write(level klog.Level, message string) {
	l.mu.RLock()
	if !l.buffering && level < l.level {
		l.mu.RUnlock()
		return
	}
	pc, file, line, ok := runtime.Caller(2)
	if strings.HasSuffix(file, "/pkg/klog/default.go") {
		pc, file, line, ok = runtime.Caller(3)
	}
	record := startupEntry{level: level, entry: zapcore.Entry{Level: zapLevel(level), Time: time.Now(), Message: message, Caller: zapcore.NewEntryCaller(pc, file, line, ok)}}
	if l.buffering {
		l.mu.RUnlock()
		l.mu.Lock()
		l.queueOrWrite(record)
		l.mu.Unlock()
	} else {
		l.emit(record)
		l.mu.RUnlock()
	}
	if level == klog.LevelFatal {
		_ = l.Close()
		os.Exit(1)
	}
}

// kitexLevel 将已校验的级别名称映射到 Kitex 的七级日志门槛。
func kitexLevel(level string) klog.Level {
	return map[string]klog.Level{"trace": klog.LevelTrace, "debug": klog.LevelDebug, "info": klog.LevelInfo, "notice": klog.LevelNotice, "warn": klog.LevelWarn, "error": klog.LevelError, "fatal": klog.LevelFatal}[level]
}

// zapLevel 保留 Kitex 的过滤语义，输出时将 Trace 映射为 Debug、Notice 映射为 Info。
func zapLevel(level klog.Level) zapcore.Level {
	switch level {
	case klog.LevelTrace, klog.LevelDebug:
		return zapcore.DebugLevel
	case klog.LevelNotice, klog.LevelInfo:
		return zapcore.InfoLevel
	case klog.LevelWarn:
		return zapcore.WarnLevel
	case klog.LevelError:
		return zapcore.ErrorLevel
	default:
		return zapcore.FatalLevel
	}
}
