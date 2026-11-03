package adapter

import (
	"context"
	"fmt"
	"github.com/cloudwego/kitex/pkg/klog"
)

// Trace 输出 Trace 级别日志。
func (l *Logger) Trace(values ...interface{}) { l.write(klog.LevelTrace, fmt.Sprint(values...)) }

// Tracef 格式化输出 Trace 级别日志。
func (l *Logger) Tracef(format string, values ...interface{}) {
	l.write(klog.LevelTrace, fmt.Sprintf(format, values...))
}

// CtxTracef 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxTracef(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelTrace, fmt.Sprintf(format, values...))
}

// Debug 输出 Debug 级别日志。
func (l *Logger) Debug(values ...interface{}) { l.write(klog.LevelDebug, fmt.Sprint(values...)) }

// Debugf 格式化输出 Debug 级别日志。
func (l *Logger) Debugf(format string, values ...interface{}) {
	l.write(klog.LevelDebug, fmt.Sprintf(format, values...))
}

// CtxDebugf 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxDebugf(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelDebug, fmt.Sprintf(format, values...))
}

// Info 输出 Info 级别日志。
func (l *Logger) Info(values ...interface{}) { l.write(klog.LevelInfo, fmt.Sprint(values...)) }

// Infof 格式化输出 Info 级别日志。
func (l *Logger) Infof(format string, values ...interface{}) {
	l.write(klog.LevelInfo, fmt.Sprintf(format, values...))
}

// CtxInfof 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxInfof(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelInfo, fmt.Sprintf(format, values...))
}

// Notice 输出 Notice 级别日志。
func (l *Logger) Notice(values ...interface{}) { l.write(klog.LevelNotice, fmt.Sprint(values...)) }

// Noticef 格式化输出 Notice 级别日志。
func (l *Logger) Noticef(format string, values ...interface{}) {
	l.write(klog.LevelNotice, fmt.Sprintf(format, values...))
}

// CtxNoticef 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxNoticef(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelNotice, fmt.Sprintf(format, values...))
}

// Warn 输出 Warn 级别日志。
func (l *Logger) Warn(values ...interface{}) { l.write(klog.LevelWarn, fmt.Sprint(values...)) }

// Warnf 格式化输出 Warn 级别日志。
func (l *Logger) Warnf(format string, values ...interface{}) {
	l.write(klog.LevelWarn, fmt.Sprintf(format, values...))
}

// CtxWarnf 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxWarnf(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelWarn, fmt.Sprintf(format, values...))
}

// Error 输出 Error 级别日志。
func (l *Logger) Error(values ...interface{}) { l.write(klog.LevelError, fmt.Sprint(values...)) }

// Errorf 格式化输出 Error 级别日志。
func (l *Logger) Errorf(format string, values ...interface{}) {
	l.write(klog.LevelError, fmt.Sprintf(format, values...))
}

// CtxErrorf 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxErrorf(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelError, fmt.Sprintf(format, values...))
}

// Fatal 输出 Fatal 级别日志，随后刷新日志并退出进程。
func (l *Logger) Fatal(values ...interface{}) { l.write(klog.LevelFatal, fmt.Sprint(values...)) }

// Fatalf 格式化输出 Fatal 级别日志，随后刷新日志并退出进程。
func (l *Logger) Fatalf(format string, values ...interface{}) {
	l.write(klog.LevelFatal, fmt.Sprintf(format, values...))
}

// CtxFatalf 兼容 Kitex 的上下文日志接口，当前不从上下文提取额外字段。
func (l *Logger) CtxFatalf(_ context.Context, format string, values ...interface{}) {
	l.write(klog.LevelFatal, fmt.Sprintf(format, values...))
}
