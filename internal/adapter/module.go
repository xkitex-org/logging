package adapter

import (
	"path"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const loggingModulePath = "go.chaoxizhongzhi.com/xkitex/logging"

// moduleResolver 根据实际构建依赖定位 Go 模块，不根据日志正文或磁盘路径猜测模块。
type moduleResolver struct {
	main  string
	paths []string
}

var (
	moduleOnce    sync.Once
	modules       moduleResolver
	callerModules sync.Map
)

// moduleForCaller 按真实调用位置解析模块并缓存结果，避免每条日志重复扫描依赖。
func moduleForCaller(pc uintptr) string {
	if module, ok := callerModules.Load(pc); ok {
		return module.(string)
	}
	moduleOnce.Do(func() {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			info = &debug.BuildInfo{}
		}
		modules = newModuleResolver(info)
	})
	module := "unknown"
	if function := runtime.FuncForPC(pc); function != nil {
		module = modules.resolve(function.Name())
	}
	callerModules.Store(pc, module)
	return module
}

// newModuleResolver 收集当前应用和依赖的模块路径，最长前缀优先以支持独立子模块。
func newModuleResolver(info *debug.BuildInfo) moduleResolver {
	paths := map[string]bool{loggingModulePath: true}
	if info.Main.Path != "" {
		paths[info.Main.Path] = true
	}
	for _, dependency := range info.Deps {
		if dependency != nil && dependency.Path != "" {
			paths[dependency.Path] = true
		}
	}
	resolver := moduleResolver{main: info.Main.Path}
	for name := range paths {
		resolver.paths = append(resolver.paths, name)
	}
	sort.Slice(resolver.paths, func(i, j int) bool {
		if len(resolver.paths[i]) == len(resolver.paths[j]) {
			return resolver.paths[i] < resolver.paths[j]
		}
		return len(resolver.paths[i]) > len(resolver.paths[j])
	})
	return resolver
}

// resolve 将完整函数名匹配到模块根，main 包使用当前可执行程序所属模块。
func (r moduleResolver) resolve(function string) string {
	if strings.HasPrefix(function, "main.") {
		if r.main != "" {
			return moduleName(r.main)
		}
		return "main"
	}
	for _, module := range r.paths {
		if strings.HasPrefix(function, module+"/") || strings.HasPrefix(function, module+".") {
			return moduleName(module)
		}
	}
	return "unknown"
}

// moduleName 使用模块末段作为展示名称，跳过 Go 的 /v2 等主版本路径后缀。
func moduleName(module string) string {
	name := path.Base(module)
	if strings.HasPrefix(name, "v") {
		if version, err := strconv.Atoi(strings.TrimPrefix(name, "v")); err == nil && version >= 2 {
			return path.Base(path.Dir(module))
		}
	}
	return name
}
