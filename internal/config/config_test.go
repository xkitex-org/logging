package config

import (
	"strings"
	"testing"
)

// TestDefaultsAndStrictValidation 验证配置缺省、合法轮转参数和非法配置的安全错误信息。
func TestDefaultsAndStrictValidation(t *testing.T) {
	for _, body := range []string{"", "{}", "level: TRACE\nformat: JSON", "file: {max_backups: 0, max_age: 0}", "file: {enabled: true, path: ./logs/application.log}", "file: {enabled: true, path: ./logs/application.log, formats: [json, console], error: true}"} {
		cfg, err := Parse([]byte(body))
		if err != nil || !cfg.Console || cfg.File.MaxSize != 100 {
			t.Fatalf("valid configuration rejected: %v", err)
		}
	}
	for _, body := range []string{
		"file: {formats: []}", "file: {formats: [json, json]}", "file: {formats: [private-value]}", "file: {formats: null}", "file: {error: null}", "file: {error: [private-value]}", "file: {filename: private-value}", "null", "[]", "level: private-value", "format: private-value", "console: false", "console: []",
		"private-value: true", "file: {private-value: true}", "file: {enabled: true}", "file: {max_size: 0}",
		"file: {max_backups: -1}", "file: {max_age: -1}", "level: null", "file: null", "file: {enabled: null}",
		"level: info\nlevel: debug", "{}\n---\n{}", "file: [private-value]", "level: [private-value",
	} {
		if _, err := Parse([]byte(body)); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("bad configuration accepted or exposed: %q %v", body, err)
		}
	}
}
