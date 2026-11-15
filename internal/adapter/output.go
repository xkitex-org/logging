package adapter

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/config"
)

// backend 将每个 Zap 输出绑定到各自的格式、级别及轮转文件，并持有可选的 SLS 发送器。
type backend struct {
	core  zapcore.Core
	files []*lumberjack.Logger
	sls   *slsSender
}

// writerSyncer 仅包装 Write，不擅自刷新终端或接管业务 Writer 的生命周期。
type writerSyncer struct{ io.Writer }

// Sync 为非持有输出提供无副作用的刷新实现。
func (writerSyncer) Sync() error { return nil }

// encoder 创建无颜色编码器；JSON 和控制台均包含模块、时间、级别及调用位置。
func encoder(format string) zapcore.Encoder {
	cfg := zap.NewProductionEncoderConfig()
	cfg.NameKey = "module"
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

// sidecarPath 在扩展名前加入类型标识，保留 X_LOGGING_FILE 的解析结果作为普通日志或单文件路径。
func sidecarPath(path, kind string) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + "." + kind + extension
}

// openLogFile 提前检查可写性，并为每个日志文件创建独立的轮转实例。
func openLogFile(path string, cfg config.File) (*lumberjack.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, errors.New("logging: cannot create log directory; check X_LOGGING_FILE and permissions")
	}
	probe, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.New("logging: cannot open log file; check X_LOGGING_FILE and permissions")
	}
	if err := probe.Close(); err != nil {
		return nil, errors.New("logging: cannot close log file during initialization")
	}
	return &lumberjack.Logger{Filename: path, MaxSize: cfg.MaxSize, MaxBackups: cfg.MaxBackups, MaxAge: cfg.MaxAge, Compress: cfg.Compress}, nil
}

// configuredBackend 一次性初始化所有输出；失败时释放已打开资源，不替换现有日志后端。
func configuredBackend(cfg config.Settings) (result *backend, err error) {
	result = &backend{}
	defer func() {
		if err != nil {
			_ = result.close()
		}
	}()
	var cores []zapcore.Core
	if cfg.Console {
		cores = append(cores, zapcore.NewCore(encoder(cfg.Format), zapcore.Lock(writerSyncer{os.Stderr}), zapcore.DebugLevel))
	}
	if cfg.File.Enabled {
		formats := cfg.File.Formats
		if formats == nil {
			formats = []string{cfg.Format}
		}
		for _, format := range formats {
			path := cfg.File.Path
			if len(formats) > 1 && format == "json" {
				path = sidecarPath(path, "json")
			}
			file, openErr := openLogFile(path, cfg.File)
			if openErr != nil {
				return result, openErr
			}
			result.files = append(result.files, file)
			cores = append(cores, zapcore.NewCore(encoder(format), zapcore.AddSync(file), zapcore.DebugLevel))
		}
		if cfg.File.Error {
			file, openErr := openLogFile(sidecarPath(cfg.File.Path, "error"), cfg.File)
			if openErr != nil {
				return result, openErr
			}
			result.files = append(result.files, file)
			cores = append(cores, zapcore.NewCore(encoder(cfg.Format), zapcore.AddSync(file), zap.LevelEnablerFunc(func(level zapcore.Level) bool { return level >= zapcore.ErrorLevel })))
		}
	}
	if cfg.SLS != nil {
		remote, openErr := newSLSCore(*cfg.SLS, cfg.Format)
		if openErr != nil {
			return result, openErr
		}
		result.sls = remote.sender
		cores = append(cores, remote)
	}
	result.core = zapcore.NewTee(cores...)
	return result, nil
}

// close 等待可选发送器并关闭每个轮转文件，不关闭控制台或业务 Writer。
func (b *backend) close() error {
	var err error
	if b.sls != nil {
		// 发送器关闭本身负责排空和等待，避免先 Sync 再 Close 重复等待两次。
		err = b.sls.close()
	} else if b.core != nil {
		err = b.core.Sync()
	}
	for _, file := range b.files {
		err = errors.Join(err, file.Close())
	}
	return err
}
