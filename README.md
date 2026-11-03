# logging

独立模块 `go.chaoxizhongzhi.com/xkitex/logging`，基于 Zap v1.28.0 和 Lumberjack v2.2.1，实现 Kitex v0.16.4 的 `klog.FullLogger`。Go 最低版本与 core 一致，为 1.25.9。

根包只公开 `New()`，内部适配、配置解析和文件轮转均放在 internal。没有 init，没有环境变量配置或自动接管行为；导入包、调用 New 都不安装全局 logger、不读取 Consul、不创建文件。

## 业务接入

```go
import (
    "github.com/cloudwego/kitex/pkg/klog"
    "go.chaoxizhongzhi.com/xkitex/core"
    "go.chaoxizhongzhi.com/xkitex/logging"
)

// main 显式选择日志实现，再由 core 加载配置并启动服务。
func main() {
    if err := x.Server(
        x.WithLogger(logging.New()),
        x.WithService(productservice.RegisterService, productHandler),
        x.WithService(categoryservice.RegisterService, categoryHandler),
    ); err != nil {
        klog.Errorf("服务退出：%v", err)
    }
}
```

core 与 logging 均通过正式 v0.0.1 模块依赖接入，两个模块各自独立，不使用 go.work 或 replace。

没传 WithLogger 时 core 保留当前 Kitex 行为，不管理 logger。传入后，core 在读取 Consul 前安装默认 Info 级别、console 格式、stderr 输出的实例；配置合并后再下发 logging 节，并在退出时释放资源。配置只应用一次，修改 Consul 后需要重启应用。

## Consul 配置

```yaml
logging:
  level: info
  format: json
  console: true
  file:
    enabled: true
    path: ./logs/application.log
    max_size: 100
    max_backups: 10
    max_age: 7
    compress: true
```

缺省配置为 level=info、format=console、console=true、file.enabled=false；文件轮转缺省为 max_size=100 MB、max_backups=10、max_age=7 天、compress=false。启用文件时必须配置 path，可使用相对当前工作目录的路径；创建父目录并提前检查文件可写性，失败时阻止服务启动。Console 和 file 至少启用一个。

level 支持 trace、debug、info、notice、warn、error、fatal；过滤保留 Kitex 七级语义，编码时 Trace 对应 Zap Debug，Notice 对应 Zap Info。format 支持 json 和 console，均输出时间、级别、调用位置及消息，不开启颜色。Ctx 方法与格式化方法行为一致，当前不自动从 context 添加 trace 或其他字段。

max_size 必须大于 0，max_backups 和 max_age 不小于 0；后两项的 0 分别表示不按数量和天数清理备份。未知字段、错误类型、null、多文档和无效值均报错，错误中不输出原始配置值。文件轮转及 gzip 压缩由 Lumberjack 完成，压缩和备份清理由其后台执行；同一日志文件只应由一个进程写入。

配置可以从 imports 合并，优先级沿用 core：后面的引用覆盖前面的引用，application.yaml 最高。只把合并后的 logging 节传给本模块，不暴露数据库、Redis 等配置。

## 输出和退出

业务和框架统一通过 `klog` 输出。配置加载过程使用默认控制台输出；文件和自定义格式从 logging 配置成功后开始生效，不回放之前的日志。配置错误保留原默认输出，不能静默退回默认后继续启动。

适配器支持 Kitex 的 SetLevel 和 SetOutput，二者与输出、配置及关闭使用同一读写锁协调；SetOutput 替换当前输出，但不接管业务传入 Writer 的 Close/Sync。正常退出和启动失败均由 core 调用 Close，先输出最后的服务结果日志再刷新和关闭文件。Close 幂等，关闭后保留控制台输出供 Server 返回后的错误处理使用。Fatal 会先写入并关闭日志，再以状态 1 退出；与 Kitex 默认 Fatal 相同，它不会走服务优雅停机。

```sh
GOWORK=off GOTOOLCHAIN=local go test -race -timeout 60s ./...
GOWORK=off GOTOOLCHAIN=local go build ./...
```
