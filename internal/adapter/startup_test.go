package adapter

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStartupReplayUsesFinalOutputAndOriginalMetadata 验证启动缓存按原顺序使用最终格式输出，保留时间和真实调用位置。
func TestStartupReplayUsesFinalOutputAndOriginalMetadata(t *testing.T) {
	l := New()
	var fallback bytes.Buffer
	l.SetOutput(&fallback)
	_, _, line, _ := runtime.Caller(0)
	l.Info("startup-first")
	l.Debug("startup-debug")
	l.Info("startup-third")
	if fallback.Len() != 0 {
		t.Fatal("startup records escaped before configuration")
	}
	time.Sleep(3 * time.Millisecond)
	configuredAt := time.Now()
	path := filepath.Join(t.TempDir(), "startup.log")
	cfg := fmt.Sprintf("level: trace\nformat: json\nconsole: false\nfile: {enabled: true, path: %q}", path)
	if err := l.ConfigureLogging([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	l.Info("after-config")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, string(data))
	want := []string{"startup-first", "startup-debug", "startup-third", "after-config"}
	if len(got) != len(want) {
		t.Fatalf("unexpected count: %d", len(got))
	}
	for i, entry := range got {
		if entry["msg"] != want[i] || entry["module"] != "logging" {
			t.Fatal("replayed records were reordered or duplicated")
		}
		if i < 3 {
			ts, err := time.Parse("2006-01-02T15:04:05.000-0700", entry["ts"].(string))
			if err != nil || !ts.Before(configuredAt) {
				t.Fatal("startup timestamp was replaced during replay")
			}
		}
	}
	if !strings.HasSuffix(got[0]["caller"].(string), fmt.Sprintf("startup_test.go:%d", line+1)) {
		t.Fatalf("caller replaced: %v", got[0]["caller"])
	}
	if fallback.Len() != 0 {
		t.Fatal("successful startup wrote fallback logs")
	}
}

// TestStartupReplayUsesConfiguredLevel 验证配置前记录的 Debug 和 Info 由最终级别决定是否输出。
func TestStartupReplayUsesConfiguredLevel(t *testing.T) {
	l := New()
	l.Debug("filtered-debug")
	l.Info("filtered-info")
	l.Warn("kept-warning")
	path := filepath.Join(t.TempDir(), "warn.log")
	if err := l.ConfigureLogging([]byte(fmt.Sprintf("level: warn\nformat: json\nconsole: false\nfile: {enabled: true, path: %q}", path))); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, string(data))
	if len(got) != 1 || got[0]["msg"] != "kept-warning" {
		t.Fatal("final level did not filter queued records")
	}
}

// TestStartupFailureAndExplicitFlush 验证更早的配置读取失败、配置错误和显式刷新都能输出默认格式，且不会重复输出。
func TestStartupFailureAndExplicitFlush(t *testing.T) {
	for _, mode := range []string{"close", "invalid config", "sync"} {
		t.Run(mode, func(t *testing.T) {
			l := New()
			var output bytes.Buffer
			l.SetOutput(&output)
			l.Info("source-marker")
			l.Error("failure-marker")
			if output.Len() != 0 {
				t.Fatal("output was not delayed")
			}
			switch mode {
			case "close":
				if err := l.Close(); err != nil {
					t.Fatal(err)
				}
			case "sync":
				if err := l.Sync(); err != nil {
					t.Fatal(err)
				}
			default:
				if err := l.ConfigureLogging([]byte("level: invalid-private-value")); err == nil {
					t.Fatal("invalid config accepted")
				}
			}
			if strings.Count(output.String(), "source-marker") != 1 || strings.Count(output.String(), "failure-marker") != 1 || strings.Contains(output.String(), "invalid-private-value") {
				t.Fatal("failure records lost, duplicated or leaked")
			}
			if strings.HasPrefix(output.String(), "{") {
				t.Fatal("failure did not use default console format")
			}
			before := output.String()
			_ = l.Close()
			if output.String() != before {
				t.Fatal("repeated close replayed records again")
			}
		})
	}
}

// TestStartupBufferLimitsFlushWithoutLoss 验证条数或容量达到上限时输出缓存和一次提示，不丢日志且不继续占用缓存。
func TestStartupBufferLimitsFlushWithoutLoss(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		l := New()
		var output bytes.Buffer
		l.SetOutput(&output)
		if oversized {
			l.Info(strings.Repeat("x", maxStartupBytes+1))
		} else {
			for i := 0; i < maxStartupEntries+2; i++ {
				l.Infof("marker-%04d", i)
			}
			for i := 0; i < maxStartupEntries+2; i++ {
				if strings.Count(output.String(), fmt.Sprintf("marker-%04d", i)) != 1 {
					t.Fatal("overflow lost or duplicated a record")
				}
			}
		}
		if len(l.startup) != 0 || l.startupBytes != 0 || l.buffering {
			t.Fatal("overflow retained an active buffer")
		}
		if strings.Count(output.String(), "启动日志缓存达到上限") != 1 {
			t.Fatal("overflow warning missing or duplicated")
		}
		_ = l.Close()
	}
}

// TestConcurrentStartupAndConfiguration 验证配置切换与并发启动日志之间不丢失、不重复且编码完整。
func TestConcurrentStartupAndConfiguration(t *testing.T) {
	l := New()
	path := filepath.Join(t.TempDir(), "concurrent.log")
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func(id int) {
			defer group.Done()
			for j := 0; j < 40; j++ {
				l.Infof("worker-%d-%d", id, j)
			}
		}(i)
	}
	if err := l.ConfigureLogging([]byte(fmt.Sprintf("format: json\nconsole: false\nfile: {enabled: true,path: %q}", path))); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	_ = l.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := entries(t, string(data))
	seen := make(map[string]bool)
	for _, entry := range got {
		msg := entry["msg"].(string)
		if seen[msg] {
			t.Fatal("duplicate concurrent record")
		}
		seen[msg] = true
	}
	if len(got) != 160 {
		t.Fatalf("missing concurrent startup records: %d", len(got))
	}
}

// TestFatalBeforeConfigurationFlushesDefault 验证尚未拿到配置时 Fatal 仍会输出启动记录并退出。
func TestFatalBeforeConfigurationFlushesDefault(t *testing.T) {
	if os.Getenv("XKITEX_FATAL_BOOTSTRAP_TEST") == "1" {
		l := New()
		l.Info("before-fatal-marker")
		l.Fatal("startup-fatal-marker")
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFatalBeforeConfigurationFlushesDefault$")
	command.Env = append(os.Environ(), "XKITEX_FATAL_BOOTSTRAP_TEST=1")
	output, err := command.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 {
		t.Fatalf("unexpected exit: %v", err)
	}
	if strings.Count(string(output), "before-fatal-marker") != 1 || strings.Count(string(output), "startup-fatal-marker") != 1 {
		t.Fatal("fatal lost or duplicated buffered startup logs")
	}
}
