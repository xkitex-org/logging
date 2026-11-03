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

没传 WithLogger 时 core 保留当前 Kitex 行为，不管理 logger。传入后，core 在读取 Consul 前安装带启动缓存的实例；配置合并后再下发 logging 节，并在退出时释放资源。配置只应用一次，修改 Consul 后需要重启应用。

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

level 支持 trace、debug、info、notice、warn、error、fatal；过滤保留 Kitex 七级语义，编码时 Trace 对应 Zap Debug，Notice 对应 Zap Info。format 支持 json 和 console，均输出模块、时间、级别、调用位置及消息，不开启颜色。Ctx 方法与格式化方法行为一致，当前不自动从 context 添加 trace 或其他字段。

max_size 必须大于 0，max_backups 和 max_age 不小于 0；后两项的 0 分别表示不按数量和天数清理备份。未知字段、错误类型、null、多文档和无效值均报错，错误中不输出原始配置值。文件轮转及 gzip 压缩由 Lumberjack 完成，压缩和备份清理由其后台执行；同一日志文件只应由一个进程写入。

配置可以从 imports 合并，优先级沿用 core：后面的引用覆盖前面的引用，application.yaml 最高。只把合并后的 logging 节传给本模块，不暴露数据库、Redis 等配置。

## 输出和退出

业务和框架统一通过 `klog` 输出。配置成功前的日志先暂存，配置成功后按原始顺序输出到最终后端，使用最终格式与级别过滤，并保留原时间和调用位置。因此配置来源、各文件读取和解析、合并等启动日志也能统一写入 JSON 文件。配置缺省时在 core 下发 nil 后以默认 Info/console 配置输出。

配置解析或文件初始化失败时，立即按默认格式输出缓存；Consul 读取等更早阶段失败时，由 core 的 Close 输出缓存和错误。显式 Sync、Fatal 或 Close 也会输出缓存。缓存最多 256 条且计入的日志数据不超过 64 KiB；达到任一上限后先输出缓存和一次提示，后续启动日志直接使用默认输出，避免无限占用内存，不静默丢弃日志。默认输出遵循当前级别，默认 Info；正常成功后按最终配置过滤，包括配置前产生的 Debug/Trace。

依赖库初始化时直接写 stderr 的警告不经过 klog，不能由本缓存统一格式。

适配器支持 Kitex 的 SetLevel 和 SetOutput，二者与输出、配置及关闭使用同一读写锁协调；SetOutput 替换当前输出，但不接管业务传入 Writer 的 Close/Sync。正常退出和启动失败均由 core 调用 Close，先输出最后的服务结果日志再刷新和关闭文件。Close 幂等，关闭后保留控制台输出供 Server 返回后的错误处理使用。Fatal 会先写入并关闭日志，再以状态 1 退出；与 Kitex 默认 Fatal 相同，它不会走服务优雅停机。

```sh
GOWORK=off GOTOOLCHAIN=local go test -race -timeout 60s ./...
GOWORK=off GOTOOLCHAIN=local go build ./...
```

## 模块标识

日志新增 module 字段，根据实际调用函数所属的 Go 模块自动识别。例如 core 输出 module=core，Kitex 输出 module=kitex，zmall/product 的业务包和 main 输出 module=product。业务继续通过 klog 输出，无需逐条传模块名。

使用编译时模块元信息进行最长路径匹配，展示模块路径最后一段，跳过 /v2 等版本后缀；无法识别时输出 unknown。模块短名相同的依赖不会自动区分命名空间。识别结果按调用位置缓存。启动日志在产生时记录模块，重放保持原模块；日志组件自身的缓存上限提示标为 logging。console 格式也显示模块列。
