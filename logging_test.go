package logging_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cloudwego/kitex/pkg/klog"

	"go.chaoxizhongzhi.com/xkitex/logging"
)

// TestNewDoesNotInstallLoggerOrReadEnvironment 验证引入及创建 logger 都不修改 Kitex 全局实现，也不根据环境变量启用能力。
func TestNewDoesNotInstallLoggerOrReadEnvironment(t *testing.T) {
	previous := klog.DefaultLogger()
	t.Setenv("X_LOG_LEVEL", "trace")
	l := logging.New()
	if klog.DefaultLogger() != previous {
		t.Fatal("New replaced the global logger")
	}
	var output bytes.Buffer
	l.SetOutput(&output)
	l.Debug("debug-marker")
	l.Info("info-marker")
	if output.Len() != 0 {
		t.Fatal("startup output was not buffered")
	}
	if err := l.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "debug-marker") || !strings.Contains(output.String(), "info-marker") {
		t.Fatal("default level depends on environment")
	}
}
