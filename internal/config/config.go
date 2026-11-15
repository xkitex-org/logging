package config

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Settings 是 logging 节的内部配置，不向业务暴露初始化或配置结构。
type Settings struct {
	Level   string `yaml:"level"`
	Format  string `yaml:"format"`
	Console bool   `yaml:"console"`
	File    File   `yaml:"file"`
	SLS     *SLS   `yaml:"sls"`
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
	// 只有显式配置 sls 节才创建默认值；缺省时不初始化凭据或发送器。
	for i := 0; i < len(node.Content[0].Content); i += 2 {
		if node.Content[0].Content[i].Value == "sls" {
			cfg.SLS = defaultSLS()
			break
		}
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
	if cfg.SLS != nil {
		if err := cfg.SLS.validate(); err != nil {
			return Settings{}, err
		}
	}
	if !cfg.Console && !cfg.File.Enabled && cfg.SLS == nil {
		return Settings{}, errors.New("logging: at least one output must be enabled")
	}
	return cfg, nil
}

// SLS 描述可选的直传目标；省略整个配置节时不启用，凭据使用阿里云默认凭据链。
type SLS struct {
	Endpoint        string        `yaml:"endpoint"`
	Project         string        `yaml:"project"`
	Logstore        string        `yaml:"logstore"`
	Topic           string        `yaml:"topic"`
	Source          string        `yaml:"source"`
	BufferSizeMB    int           `yaml:"buffer_size_mb"`
	BatchSize       int           `yaml:"batch_size"`
	FlushInterval   time.Duration `yaml:"flush_interval"`
	Retries         int           `yaml:"retries"`
	Timeout         time.Duration `yaml:"timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// defaultSLS 为可选发送器提供有界缓存及等待时间，业务只需填写发送目标。
func defaultSLS() *SLS {
	return &SLS{BufferSizeMB: 8, BatchSize: 1024, FlushInterval: time.Second, Retries: 3, Timeout: 3 * time.Second, ShutdownTimeout: 5 * time.Second}
}

// validate 拒绝不完整目标和无效参数，错误只包含字段名，不包含凭据或配置原文。
func (s *SLS) validate() error {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	s.Project = strings.TrimSpace(s.Project)
	s.Logstore = strings.TrimSpace(s.Logstore)
	if s.Endpoint == "" || s.Project == "" || s.Logstore == "" {
		return errors.New("logging: sls.endpoint, sls.project and sls.logstore are required")
	}
	if !strings.Contains(s.Endpoint, "://") {
		s.Endpoint = "https://" + s.Endpoint
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return errors.New("logging: sls.endpoint must be an HTTP(S) endpoint without credentials, path or query")
	}
	s.Endpoint = strings.TrimSuffix(s.Endpoint, "/")
	if strings.IndexFunc(s.Project, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') }) >= 0 || strings.ContainsAny(s.Project+s.Logstore, "/\\?# \t\r\n|\x00") || strings.ContainsAny(s.Topic+s.Source, "|\r\n\x00") {
		return errors.New("logging: invalid sls project, logstore, topic or source")
	}
	if s.BufferSizeMB < 1 || s.BufferSizeMB > 1024 || s.BatchSize < 1 || s.BatchSize > 40960 || s.Retries < 0 || s.Retries > 100 {
		return errors.New("logging: sls.buffer_size_mb must be 1..1024, batch_size 1..40960 and retries 0..100")
	}
	if s.FlushInterval < 100*time.Millisecond || s.FlushInterval > time.Minute || s.Timeout < time.Millisecond || s.Timeout > time.Minute || s.ShutdownTimeout < time.Millisecond || s.ShutdownTimeout > time.Minute {
		return errors.New("logging: invalid sls.flush_interval (100ms..1m), timeout or shutdown_timeout (1ms..1m)")
	}
	return nil
}

// containsNull 拒绝会被结构体解码静默当作缺省的 null，包含嵌套字段。
func containsNull(node *yaml.Node) bool {
	if node.Tag == "!!null" {
		return true
	}
	return slices.ContainsFunc(node.Content, containsNull)
}
