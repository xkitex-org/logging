// Package logging 提供供 core.WithLogger 显式接入的 Zap 日志实现，引入包本身不产生副作用。
package logging

import (
	"github.com/cloudwego/kitex/pkg/klog"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/adapter"
)

// New 创建带有界启动日志缓存的 logger，不读取环境变量或配置，不安装全局 logger 或创建文件。
// 传给 core.WithLogger 后，由 core 下发 logging 节，并在退出时调用该实例的 Close。
func New() klog.FullLogger {
	return adapter.New()
}
