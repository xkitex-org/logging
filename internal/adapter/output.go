package adapter

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/config"
)

// backend 将 Zap 编码及输出与可选文件资源绑定，受 Logger 的读写锁保护。
type backend struct {
	core zapcore.Core
	file *lumberjack.Logger
}

// writerSyncer 仅包装 Write，不擅自刷新终端或接管业务 Writer 的生命周期。
type writerSyncer struct{ io.Writer }

// Sync 为非持有输出提供无副作用的刷新实现。
func (writerSyncer) Sync() error { return nil }

// encoder 创建无颜色编码器；JSON 和控制台均包含时间、级别及调用位置。
func encoder(format string) zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	if format == "json" {
		return zapcore.NewJSONEncoder(cfg)
	}
	return zapcore.NewConsoleEncoder(cfg)
}

// writerBackend 创建指定 Writer 的并发安全输出，默认接受全部已由 Kitex 过滤的级别。
func writerBackend(cfg config.Settings, writer io.Writer) *backend {
	return &backend{core: zapcore.NewCore(encoder(cfg.Format), zapcore.Lock(writerSyncer{writer}), zapcore.DebugLevel)}
}

// configuredBackend 在启动期间提前检查日志文件可写性，避免服务监听后才发现路径或权限错误。
func configuredBackend(cfg config.Settings) (*backend, error) {
	var sinks []zapcore.WriteSyncer
	if cfg.Console {
		sinks = append(sinks, zapcore.Lock(writerSyncer{os.Stderr}))
	}
	var file *lumberjack.Logger
	if cfg.File.Enabled {
		if err := os.MkdirAll(filepath.Dir(cfg.File.Path), 0o750); err != nil {
			return nil, errors.New("logging: cannot create log directory; check file.path and permissions")
		}
		probe, err := os.OpenFile(cfg.File.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, errors.New("logging: cannot open log file; check file.path and permissions")
		}
		if err := probe.Close(); err != nil {
			return nil, errors.New("logging: cannot close log file during initialization")
		}
		file = &lumberjack.Logger{Filename: cfg.File.Path, MaxSize: cfg.File.MaxSize, MaxBackups: cfg.File.MaxBackups, MaxAge: cfg.File.MaxAge, Compress: cfg.File.Compress}
		sinks = append(sinks, zapcore.AddSync(file))
	}
	return &backend{core: zapcore.NewCore(encoder(cfg.Format), zapcore.NewMultiWriteSyncer(sinks...), zapcore.DebugLevel), file: file}, nil
}

// close 刷新 Zap 输出后关闭持有的轮转文件，不关闭控制台或业务 Writer。
func (b *backend) close() error {
	err := b.core.Sync()
	if b.file != nil {
		err = errors.Join(err, b.file.Close())
	}
	return err
}
