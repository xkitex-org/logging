package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Settings 是 logging 节的内部配置，不向业务暴露初始化或配置结构。
type Settings struct {
	Level   string `yaml:"level"`
	Format  string `yaml:"format"`
	Console bool   `yaml:"console"`
	File    File   `yaml:"file"`
}

// File 描述单进程文件输出及 Lumberjack 轮转设置，大小单位为 MB，保留时间单位为天。
type File struct {
	Enabled    bool     `yaml:"enabled"`
	Formats    []string `yaml:"formats"`
	Error      bool     `yaml:"error"`
	Path       string   `yaml:"-"`
	MaxSize    int      `yaml:"max_size"`
	MaxBackups int      `yaml:"max_backups"`
	MaxAge     int      `yaml:"max_age"`
	Compress   bool     `yaml:"compress"`
}

// Defaults 返回无文件输出的默认配置，不依赖环境变量。
func Defaults() Settings {
	return Settings{Level: "info", Format: "console", Console: true, File: File{Path: "/logs/app.log", MaxSize: 100, MaxBackups: 10, MaxAge: 7}}
}

// Parse 解析 logging 节，并在启动配置时读取一次 X_LOGGING_FILE；路径不接受 YAML 配置。
// 拒绝非法字段、null 和多文档，错误中不附带原始值。
func Parse(body []byte) (Settings, error) {
	cfg := Defaults()
	if path := os.Getenv("X_LOGGING_FILE"); path != "" {
		cfg.File.Path = path
	}
	if len(body) == 0 {
		return cfg, nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode || containsNull(&node) {
		return Settings{}, errors.New("logging: configuration must be a YAML object without null values")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Settings{}, errors.New("logging: unknown field, duplicate field or invalid field type")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Settings{}, errors.New("logging: configuration must contain exactly one YAML document")
	}
	cfg.Level = strings.ToLower(strings.TrimSpace(cfg.Level))
	switch cfg.Level {
	case "trace", "debug", "info", "notice", "warn", "error", "fatal":
	default:
		return Settings{}, errors.New("logging: level must be trace, debug, info, notice, warn, error or fatal")
	}
	cfg.Format = strings.ToLower(strings.TrimSpace(cfg.Format))
	if cfg.Format != "json" && cfg.Format != "console" {
		return Settings{}, errors.New("logging: format must be json or console")
	}
	if cfg.File.Formats != nil {
		if len(cfg.File.Formats) == 0 {
			return Settings{}, errors.New("logging: file.formats must not be empty")
		}
		seen := make(map[string]bool)
		for i, format := range cfg.File.Formats {
			format = strings.ToLower(strings.TrimSpace(format))
			if format != "json" && format != "console" || seen[format] {
				return Settings{}, errors.New("logging: file.formats must contain distinct json or console values")
			}
			cfg.File.Formats[i] = format
			seen[format] = true
		}
	}
	if cfg.File.MaxSize <= 0 || cfg.File.MaxBackups < 0 || cfg.File.MaxAge < 0 {
		return Settings{}, errors.New("logging: file.max_size must be positive; file.max_backups and file.max_age must be nonnegative")
	}
	if cfg.File.Enabled && (strings.TrimSpace(cfg.File.Path) == "" || strings.ContainsRune(cfg.File.Path, '\x00')) {
		return Settings{}, errors.New("logging: X_LOGGING_FILE must name a valid file when file output is enabled")
	}
	if !cfg.Console && !cfg.File.Enabled {
		return Settings{}, errors.New("logging: at least one output must be enabled")
	}
	return cfg, nil
}

// containsNull 拒绝会被结构体解码静默当作缺省的 null，包含嵌套字段。
func containsNull(node *yaml.Node) bool {
	if node.Tag == "!!null" {
		return true
	}
	return slices.ContainsFunc(node.Content, containsNull)
}
