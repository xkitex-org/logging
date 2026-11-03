package adapter

import (
	"runtime/debug"
	"testing"
)

// TestModuleResolution 验证根包、子包、main、独立子模块和主版本路径的归属，不使用消息内容猜测。
func TestModuleResolution(t *testing.T) {
	resolver := newModuleResolver(&debug.BuildInfo{
		Main: debug.Module{Path: "go.chaoxizhongzhi.com/zmall/product"},
		Deps: []*debug.Module{
			{Path: "go.chaoxizhongzhi.com/xkitex/core"},
			{Path: "github.com/cloudwego/kitex"},
			{Path: "example.com/common"},
			{Path: "example.com/common/addon"},
			{Path: "example.com/client/v2"},
			{Path: "example.com/original", Replace: &debug.Module{Path: "example.com/replacement"}},
			nil,
		},
	})
	for _, tc := range []struct{ function, want string }{
		{"go.chaoxizhongzhi.com/xkitex/core.Server", "core"},
		{"go.chaoxizhongzhi.com/xkitex/core/internal/config.LoadWithLogging", "core"},
		{"github.com/cloudwego/kitex/pkg/remote/remotesvr.(*server).Start", "kitex"},
		{"go.chaoxizhongzhi.com/zmall/product/internal/handler.(*ProductHandler).GetProduct", "product"},
		{"main.main", "product"},
		{"go.chaoxizhongzhi.com/xkitex/logging/internal/adapter.(*Logger).Close", "logging"},
		{"example.com/common/addon/internal.Run", "addon"},
		{"example.com/commonbase.Other", "unknown"},
		{"example.com/client/v2.Call", "client"},
		{"example.com/original.Foo", "original"},
		{"runtime.goexit", "unknown"},
		{"", "unknown"},
	} {
		if got := resolver.resolve(tc.function); got != tc.want {
			t.Fatalf("module for %s = %s, want %s", tc.function, got, tc.want)
		}
	}
	if got := newModuleResolver(&debug.BuildInfo{}).resolve("main.main"); got != "main" {
		t.Fatal("missing main module metadata not handled")
	}
	if got := moduleForCaller(0); got != "unknown" {
		t.Fatal("unknown program counter not handled")
	}
}
