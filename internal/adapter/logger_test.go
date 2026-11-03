package adapter

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/kitex/pkg/klog"
)

// jsonLogger 创建已配置 JSON 的内存输出，供接口和并发测试使用。
func jsonLogger(t *testing.T) (*Logger, *bytes.Buffer) {
	t.Helper()
	l := New()
	if err := l.ConfigureLogging([]byte("format: json\nlevel: trace")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	l.SetOutput(&output)
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	})
	return l, &output
}

// entries 解析真实日志 JSON，避免将格式化输出仅当作字符串断言。
func entries(t *testing.T, output string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		result = append(result, entry)
	}
	return result
}

// TestKitexMethodsLevelsAndCaller 验证各类方法、Trace/Notice 的过滤及直接调用和 klog 调用的真实位置。
func TestKitexMethodsLevelsAndCaller(t *testing.T) {
	l, output := jsonLogger(t)
	l.Trace("trace")
	l.Debugf("debug-%d", 1)
	l.CtxInfof(context.Background(), "info-%d", 2)
	l.SetLevel(klog.LevelNotice)
	l.Info("filtered")
	l.Notice("notice")
	l.Warnf("warn-%d", 3)
	l.CtxErrorf(nil, "error-%d", 4)
	previous := klog.DefaultLogger()
	klog.SetLogger(l)
	klog.Errorf("global-%d", 5)
	klog.SetLogger(previous)
	got := entries(t, output.String())
	want := []string{"trace", "debug-1", "info-2", "notice", "warn-3", "error-4", "global-5"}
	if len(got) != len(want) {
		t.Fatalf("wrong entry count: %d", len(got))
	}
	for i, entry := range got {
		if entry["msg"] != want[i] || entry["ts"] == nil || entry["module"] != "logging" || !strings.Contains(entry["caller"].(string), "adapter/logger_test.go:") {
			t.Fatalf("incorrect message or caller: %v", entry)
		}
	}
	if got[0]["level"] != "debug" || got[3]["level"] != "info" {
		t.Fatal("incorrect Trace/Notice encoding")
	}
}

// TestFileRotationCompressionAndClose 验证日志真实写入、按大小轮转并压缩，关闭后不会重新打开文件。
func TestFileRotationCompressionAndClose(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "nested", "application.log")
	l := New()
	body := fmt.Sprintf("format: json\nconsole: false\nfile: {enabled: true, path: %q, max_size: 1, max_backups: 2, max_age: 7, compress: true}", filename)
	if err := l.ConfigureLogging([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatal("file path was not checked during initialization")
	}
	for i := 0; i < 4; i++ {
		l.Infof("marker-%d %s", i, strings.Repeat("x", 400000))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var backups []string
	for time.Now().Before(deadline) {
		backups, _ = filepath.Glob(filepath.Join(filepath.Dir(filename), "application-*.log.gz"))
		if len(backups) == 1 {
			// Lumberjack 先写 gzip 再删除源文件；源文件消失才表示压缩已经完成。
			if _, err := os.Stat(strings.TrimSuffix(backups[0], ".gz")); os.IsNotExist(err) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one compressed rotation, got %d", len(backups))
	}
	file, err := os.Open(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	older, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	all := append(older, current...)
	for i := 0; i < 4; i++ {
		if !bytes.Contains(all, []byte(fmt.Sprintf("marker-%d", i))) {
			t.Fatal("rotation lost a log entry")
		}
	}
	var after bytes.Buffer
	l.SetOutput(&after)
	l.Error("after-close")
	unchanged, _ := os.ReadFile(filename)
	if !bytes.Equal(current, unchanged) || !strings.Contains(after.String(), "after-close") {
		t.Fatal("closed file reopened or returned-server logs were lost")
	}
	if err := l.ConfigureLogging(nil); err == nil {
		t.Fatal("closed logger was configured again")
	}
}

// TestConfigurationFailureKeepsDefaultAndReportsSafeError 验证非法配置及不可写路径不破坏默认输出，也不泄露配置值。
func TestConfigurationFailureKeepsDefaultAndReportsSafeError(t *testing.T) {
	l := New()
	var output bytes.Buffer
	l.SetOutput(&output)
	if err := l.ConfigureLogging([]byte("level: private-value")); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatal("invalid configuration accepted or exposed")
	}
	dir := t.TempDir()
	if err := l.ConfigureLogging([]byte(fmt.Sprintf("file: {enabled: true, path: %q}", dir))); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatal("directory accepted as a log file or path exposed in error")
	}
	l.Info("default-still-works")
	if !strings.Contains(output.String(), "default-still-works") {
		t.Fatal("failed configuration replaced the working backend")
	}
	if err := l.ConfigureLogging(nil); err != nil {
		t.Fatal(err)
	}
	if err := l.ConfigureLogging(nil); err == nil {
		t.Fatal("configuration applied twice")
	}
	_ = l.Close()
}

// TestConcurrentOutputAndControls 验证输出、级别、Writer 切换及刷新并发执行时没有竞争或丢失编码边界。
func TestConcurrentOutputAndControls(t *testing.T) {
	l, output := jsonLogger(t)
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				l.Infof("entry-%d", j)
			}
		}()
	}
	for i := 0; i < 50; i++ {
		l.SetLevel(klog.LevelTrace)
		l.SetOutput(output)
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	group.Wait()
	if count := len(entries(t, output.String())); count != 600 {
		t.Fatalf("lost concurrent entries: %d", count)
	}
}

// TestFatalFlushesFileAndExits 验证 Fatal 写入并关闭文件后以状态 1 退出，不依赖 defer 执行。
func TestFatalFlushesFileAndExits(t *testing.T) {
	if filename := os.Getenv("XKITEX_FATAL_TEST_FILE"); filename != "" {
		l := New()
		if err := l.ConfigureLogging([]byte(fmt.Sprintf("format: json\nconsole: false\nfile: {enabled: true, path: %q}", filename))); err != nil {
			panic(err)
		}
		l.Fatalf("fatal-%d", 42)
		return
	}
	filename := filepath.Join(t.TempDir(), "fatal.log")
	command := exec.Command(os.Args[0], "-test.run=^TestFatalFlushesFileAndExits$")
	command.Env = append(os.Environ(), "XKITEX_FATAL_TEST_FILE="+filename)
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("unexpected fatal exit: %v", err)
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, string(body))
	if len(got) != 1 || got[0]["msg"] != "fatal-42" || got[0]["level"] != "fatal" {
		t.Fatal("fatal entry was not flushed")
	}
}
