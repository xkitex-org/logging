package adapter

import (
	"os"

	"github.com/cloudwego/kitex/pkg/klog"
	"go.uber.org/zap/zapcore"
)

const (
	maxStartupEntries = 256
	maxStartupBytes   = 64 << 10
)

// startupEntry 保存原始时间和调用位置，最终级别在实际输出时判断。
type startupEntry struct {
	level klog.Level
	entry zapcore.Entry
}

// emit 将日志输出到当前后端，调用方必须持有读锁或写锁。
func (l *Logger) emit(record startupEntry) {
	if record.level < l.level {
		return
	}
	if checked := l.backend.core.Check(record.entry, nil); checked != nil {
		checked.ErrorOutput = zapcore.Lock(writerSyncer{os.Stderr})
		checked.Write()
	}
}

// queueOrWrite 在配置前暂存日志，超出上限时一次性输出缓存并转为默认输出，不丢弃记录。
func (l *Logger) queueOrWrite(record startupEntry) {
	if l.buffering {
		size := len(record.entry.Message) + len(record.entry.Caller.File) + len(record.entry.Caller.Function) + 128
		if len(l.startup) < maxStartupEntries && size <= maxStartupBytes-l.startupBytes {
			l.startup = append(l.startup, record)
			l.startupBytes += size
			return
		}
		l.flushStartup()
		l.buffering = false
		l.emit(startupEntry{level: klog.LevelWarn, entry: zapcore.Entry{Level: zapcore.WarnLevel, Time: record.entry.Time, Message: "logging: 启动日志缓存达到上限，后续启动日志使用默认输出"}})
	}
	l.emit(record)
}

// flushStartup 按缓存顺序输出，保留原时间和调用位置；调用方必须持有写锁。
func (l *Logger) flushStartup() {
	for _, record := range l.startup {
		l.emit(record)
	}
	l.startup = nil
	l.startupBytes = 0
}

// finishStartup 在配置错误时立即输出默认格式缓存，使随后记录的错误直接可见。
func (l *Logger) finishStartup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushStartup()
	l.buffering = false
}
