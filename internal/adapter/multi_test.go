package adapter

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFourOutputsAndStartupReplay 验证控制台普通格式、两份全量文件、错误分流以及启动缓存的同步重放。
func TestFourOutputsAndStartupReplay(t *testing.T) {
	dir := t.TempDir()
	console, err := os.Create(filepath.Join(dir, "console.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	previous := os.Stderr
	os.Stderr = console
	defer func() { os.Stderr = previous }()
	path := filepath.Join(dir, "application.log")
	t.Setenv("X_LOGGING_FILE", path)
	l := New()
	l.Info("startup-info-marker")
	l.Error("startup-error-marker")
	body := "format: console\nconsole: true\nfile: {enabled: true, formats: [json, console], error: true}"
	if err := l.ConfigureLogging([]byte(body)); err != nil {
		t.Fatal(err)
	}
	l.Info("runtime-info-marker")
	l.Warn("runtime-warn-marker")
	l.Error("runtime-error-marker")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	structured := entries(t, read(sidecarPath(path, "json")))
	plain, terminal, errors := read(path), read(console.Name()), read(sidecarPath(path, "error"))
	if len(structured) != 5 {
		t.Fatalf("lost or repeated JSON entries: %d", len(structured))
	}
	for _, entry := range structured {
		message := entry["msg"].(string)
		if entry["module"] != "logging" || entry["caller"] == nil || entry["ts"] == nil || strings.Count(plain, message) != 1 || strings.Count(terminal, message) != 1 {
			t.Fatalf("inconsistent fanout for %s", message)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(terminal), "\n") {
		if json.Valid([]byte(line)) {
			t.Fatal("console unexpectedly used JSON")
		}
	}
	if strings.Count(errors, "-error-marker") != 2 || strings.Contains(errors, "-info-marker") || strings.Contains(errors, "-warn-marker") {
		t.Fatal("error file did not isolate error severity")
	}
	before := read(path)
	l.Error("after-close-marker")
	if read(path) != before || !strings.Contains(read(console.Name()), "after-close-marker") {
		t.Fatal("closed files reopened or console fallback failed")
	}
}

// TestAllFilesRotateIndependently 验证三份文件分别轮转、压缩并保留全部错误记录。
func TestAllFilesRotateIndependently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	t.Setenv("X_LOGGING_FILE", path)
	l := New()
	cfg := "format: console\nconsole: false\nfile: {enabled: true, formats: [json, console], error: true, max_size: 1, max_backups: 2, compress: true}"
	if err := l.ConfigureLogging([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		l.Errorf("rotation-marker-%d %s", i, strings.Repeat("x", 400000))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, sidecarPath(path, "json"), sidecarPath(path, "error")} {
		extension := filepath.Ext(file)
		pattern := strings.TrimSuffix(file, extension) + "-*" + extension + ".gz"
		var backups []string
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			backups, _ = filepath.Glob(pattern)
			if len(backups) == 1 {
				if _, err := os.Stat(strings.TrimSuffix(backups[0], ".gz")); os.IsNotExist(err) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(backups) != 1 {
			t.Fatalf("missing independent rotation: %s", filepath.Base(file))
		}
		source, err := os.Open(backups[0])
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(source)
		if err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
		old, err := io.ReadAll(reader)
		_ = reader.Close()
		_ = source.Close()
		if err != nil {
			t.Fatal(err)
		}
		current, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		all := append(old, current...)
		for i := 0; i < 4; i++ {
			if bytes.Count(all, []byte(fmt.Sprintf("rotation-marker-%d", i))) != 1 {
				t.Fatal("file rotation lost or duplicated an entry")
			}
		}
	}
}

// TestCompanionFailureKeepsExistingBackend 验证任意伴随文件不可写时整体配置失败，并保留可用的默认输出。
func TestCompanionFailureKeepsExistingBackend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	t.Setenv("X_LOGGING_FILE", path)
	if err := os.Mkdir(sidecarPath(path, "json"), 0o700); err != nil {
		t.Fatal(err)
	}
	l := New()
	var fallback bytes.Buffer
	l.SetOutput(&fallback)
	cfg := "file: {enabled: true, formats: [console, json], error: true}"
	if err := l.ConfigureLogging([]byte(cfg)); err == nil || strings.Contains(err.Error(), path) {
		t.Fatal("invalid companion accepted or path leaked")
	}
	l.Error("fallback-marker")
	if !strings.Contains(fallback.String(), "fallback-marker") {
		t.Fatal("failed multi-file setup replaced existing output")
	}
	_ = l.Close()
}

// TestMultiFileFatalFlushes 验证 Fatal 在进程退出前写入并关闭全部三份日志文件。
func TestMultiFileFatalFlushes(t *testing.T) {
	if path := os.Getenv("XKITEX_MULTI_FATAL_TEST_FILE"); path != "" {
		t.Setenv("X_LOGGING_FILE", path)
		l := New()
		cfg := "format: console\nconsole: false\nfile: {enabled: true, formats: [json, console], error: true}"
		if err := l.ConfigureLogging([]byte(cfg)); err != nil {
			panic(err)
		}
		l.Fatal("multi-fatal-marker")
		return
	}
	path := filepath.Join(t.TempDir(), "fatal.log")
	t.Setenv("X_LOGGING_FILE", path)
	child := exec.Command(os.Args[0], "-test.run=^TestMultiFileFatalFlushes$")
	child.Env = append(os.Environ(), "XKITEX_MULTI_FATAL_TEST_FILE="+path)
	err := child.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("wrong fatal exit: %v", err)
	}
	for _, file := range []string{path, sidecarPath(path, "json"), sidecarPath(path, "error")} {
		body, err := os.ReadFile(file)
		if err != nil || bytes.Count(body, []byte("multi-fatal-marker")) != 1 {
			t.Fatal("Fatal did not flush all file outputs")
		}
	}
}
