package config

import (
	"strings"
	"testing"
	"time"
)

// TestSLSDefaultsAndOverrides 验证缺省不启用、最小目标配置及显式零值覆盖。
func TestSLSDefaultsAndOverrides(t *testing.T) {
	for _, body := range []string{"", "{}", "level: warn"} {
		cfg, err := Parse([]byte(body))
		if err != nil || cfg.SLS != nil || !cfg.Console || cfg.File.Enabled || cfg.File.Path != "/logs/app.log" {
			t.Fatalf("unexpected default output: %+v %v", cfg, err)
		}
	}
	body := "sls: {endpoint: cn-wulanchabu.log.aliyuncs.com, project: example, logstore: application}"
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.SLS
	if s.Endpoint != "https://cn-wulanchabu.log.aliyuncs.com" || s.BufferSizeMB != 8 || s.BatchSize != 1024 || s.FlushInterval != time.Second || s.Retries != 3 || s.Timeout != 3*time.Second || s.ShutdownTimeout != 5*time.Second || !cfg.Console {
		t.Fatalf("wrong SLS defaults: %+v", s)
	}
	cfg, err = Parse([]byte("console: false\nsls: {endpoint: https://example.com/, project: example, logstore: application, retries: 0, batch_size: 1, flush_interval: 100ms, timeout: 500ms, shutdown_timeout: 2s}"))
	if err != nil || cfg.Console || cfg.SLS.Retries != 0 || cfg.SLS.BatchSize != 1 || cfg.SLS.Timeout != 500*time.Millisecond || cfg.SLS.Endpoint != "https://example.com" {
		t.Fatalf("override rejected: %+v %v", cfg, err)
	}
}

// TestSLSInvalidConfiguration 验证配置块存在时不可缺目标，参数与未知字段仍严格校验。
func TestSLSInvalidConfiguration(t *testing.T) {
	for _, body := range []string{
		"sls: {}", "sls: null", "sls: false", "sls: []",
		"sls: {enabled: false}", "sls: {access_key_secret: private-value}",
		"sls: {endpoint: ftp://example.com, project: example, logstore: application}",
		"sls: {endpoint: 'https://user:private-value@example.com', project: example, logstore: application}",
		"sls: {endpoint: 'https://example.com/private-value', project: example, logstore: application}",
		"sls: {endpoint: 'https://example.com?secret=private-value', project: example, logstore: application}",
		"sls: {endpoint: example.com, project: 'a/b', logstore: application}",
		"sls: {endpoint: example.com, project: example, logstore: application, buffer_size_mb: 0}",
		"sls: {endpoint: example.com, project: example, logstore: application, batch_size: 0}",
		"sls: {endpoint: example.com, project: example, logstore: application, retries: -1}",
		"sls: {endpoint: example.com, project: example, logstore: application, flush_interval: 0s}",
		"sls: {endpoint: example.com, project: example, logstore: application, timeout: 0s}",
		"sls: {endpoint: example.com, project: example, logstore: application, shutdown_timeout: 0s}",
	} {
		if _, err := Parse([]byte(body)); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("invalid config accepted or exposed: %s %v", body, err)
		}
	}
}
