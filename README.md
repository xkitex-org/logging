# logging：日志配置与输出

独立模块 `go.chaoxizhongzhi.com/xkitex/logging`，基于 Zap v1.28.0 和 Lumberjack v2.2.1，实现 Kitex v0.16.4 的 `klog.FullLogger`。Go 最低版本与 core 一致，为 1.25.9。

根包只公开 `New()`，内部适配、配置解析和文件轮转均放在 `internal`。不使用 `init()` 自动接管日志，仅文件路径从 `X_LOGGING_FILE` 读取，其余日志配置由 core 下发；导入包、调用 `New()` 都不安装全局日志实现、不读取 Consul、不创建文件。

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

本包版本为 v0.0.2；core 保持独立的正式模块依赖，两个模块不使用 go.work 或 replace。

没传 `WithLogger` 时，core 保留当前 Kitex 行为，不管理日志实现。传入后，core 在读取 Consul 前安装带启动缓存的实例；配置合并后再下发 logging 节，并在退出时释放资源。配置只应用一次，修改 Consul 或 X_LOGGING_FILE 后需要重启应用。文件路径在 ConfigureLogging 时读取一次，调用 New 不读取环境变量。

## Consul 配置

不配置 `logging` 即使用默认的 Info 级别和普通格式控制台输出，不创建文件、不连接 SLS。所有配置均先应用默认值，只填写需要覆盖的字段。例如只修改级别：

```yaml
logging:
  level: warn
```

同时保留普通文件、JSON 文件和独立错误文件，只需：

```yaml
logging:
  file:
    enabled: true
    formats: [json, console]
    error: true
```

缺省配置为 level=info、format=console、console=true、file.enabled=false；文件轮转缺省为 max_size=100 MB、max_backups=10、max_age=7 天、compress=false。文件路径仅由环境变量 `X_LOGGING_FILE` 指定，未设置或为空时默认 `/logs/app.log`；可使用相对当前工作目录的路径。Consul 不再支持 `logging.file.path`，旧字段会按未知字段报错；创建父目录并提前检查文件可写性，失败时阻止服务启动。控制台、文件和 SLS 至少保留一种输出。

level 支持 trace、debug、info、notice、warn、error、fatal；过滤保留 Kitex 七级语义，编码时 Trace 对应 Zap Debug，Notice 对应 Zap Info。format 支持 json 和 console，控制控制台及未单独指定格式时的文件编码；均输出模块、时间、级别、调用位置及消息，不开启颜色。Ctx 方法与格式化方法行为一致，当前不自动从上下文添加追踪标识或其他字段。

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

每条日志的 `module` 字段表示实际调用方所属的 Go 模块，业务继续使用 `klog`，无需逐条指定模块名。

| 调用方 | module 值 |
| --- | --- |
| `go.chaoxizhongzhi.com/xkitex/core` 及其内部包 | `core` |
| `github.com/cloudwego/kitex` 及其内部包 | `kitex` |
| `go.chaoxizhongzhi.com/zmall/product` 的业务包和 main | `product` |
| 日志组件自身的缓存上限提示 | `logging` |

识别过程如下：

1. 沿用 `caller` 的真实调用位置，跳过 klog 包装层，取得程序计数器。
2. 通过 `runtime.FuncForPC` 取得完整函数名，例如 `go.chaoxizhongzhi.com/xkitex/core/internal/config.LoadWithLogging`。
3. 使用 `debug.ReadBuildInfo` 中的当前应用及依赖模块路径，按最长前缀匹配模块根；因此独立子模块优先于上层模块。
4. 取模块路径最后一段作为展示名称，跳过 `/v2` 等主版本后缀。`main.main` 等主包函数单独归入当前应用模块。
5. 使用 Zap 日志条目的 `LoggerName` 保存模块，将编码器的名称字段设为 `module`，使 JSON 和控制台输出都包含该标识。

模块信息只读取一次，调用位置的识别结果通过 `sync.Map` 缓存，后续同一位置直接使用缓存。启动日志产生时就保存模块名，重放时保持原值。

这是按 Go 模块归属划分：core 中的 config、startup 等包都显示 `core`。无法识别时输出 `unknown`；模块短名相同的不同依赖不会自动区分命名空间。识别不读取日志正文，也不从本机源码目录或模块缓存目录猜测模块。


## 多文件与错误分流

`X_LOGGING_FILE` 是唯一文件路径入口，默认 `/logs/app.log`；开启文件并设置 `formats: [json, console]`、`error: true` 后，上面的示例会生成：

| 输出 | 路径或位置 | 内容 |
| --- | --- | --- |
| 普通全量日志 | `/logs/app.log` | 通过全局级别过滤的全部日志，普通文本 |
| JSON 全量日志 | `/logs/app.json.log` | 同一批日志，JSON 每行一条 |
| 错误专用日志 | `/logs/app.error.log` | Error 及 Fatal，格式沿用 logging.format |
| 控制台 | stderr | 格式沿用 logging.format，示例为普通文本 |

Error 同时保留在两份全量文件，不会从全量日志中移除。三个文件各自使用独立的 Lumberjack 轮转器，共用配置中的轮转大小、保留数量、天数和压缩策略。启动缓存重放、module/caller/time、级别过滤、Fatal 刷新及 Close 同样覆盖全部输出。

formats 可选择 json、console 或两者，不能重复或显式设置为空列表。不配置 formats 时保持原单文件行为，使用 logging.format 写入 X_LOGGING_FILE 对应路径；error 缺省为 false。只选择一种格式时仍使用 X_LOGGING_FILE 对应路径，同时选择两种时普通文本使用原路径、JSON 在扩展名前加 `.json`。错误文件在扩展名前加 `.error`；没有扩展名时直接追加相应后缀。

任一输出文件不能初始化时整体配置失败，保留原输出；不会静默跳过某份文件。SetOutput 会替换全部文件输出并关闭持有文件，Close 后只保留控制台回退输出。

共享的格式与轮转参数继续放在 Consul 的 logging 节中，由 core 合并后下发；文件路径不再由 application.yaml 或 imports 提供。应用和容器启动脚本统一使用 `X_LOGGING_FILE`，例如：

```sh
export X_LOGGING_FILE=/logs/app.log
```

容器挂载 `/logs` 保存日志；需要改变文件位置时覆盖 X_LOGGING_FILE，普通、JSON 与错误文件会按同一基准路径派生。文件开关、格式和轮转参数仍由 Consul 控制，设置路径不会自动开启文件输出。

发布切换时，先准备新版 logging 正式依赖和应用镜像，再协调删除合并配置中的旧 `logging.file.path`（包括 application.yaml 和 imports）并启动新版。旧版 logging 仍依赖 YAML path，不能提前仅删 Consul 字段。应用是否已更新正式依赖和完成环境配置切换应单独核对；发布模块不代表生产已切换。

## SLS 可选直传

存在 `logging.sls` 配置块时，增加一个 SLS 输出；省略该块时不创建发送器、不初始化凭据链。没有 `sls.enabled` 开关，也不需要更改 `logging.New()` 或业务 klog 调用。最少配置三个目标字段：

```yaml
logging:
  sls:
    endpoint: cn-wulanchabu.log.aliyuncs.com
    project: your-production-project
    logstore: application
```

控制台仍按默认普通格式输出。SLS 上传为结构化字段，不受 `logging.format` 影响，保留 `level、ts、module、caller、msg` 和原始时间。SLS 的 Project、Logstore 和索引由运维预先配置；组件不创建或修改云端资源。Endpoint 不带协议时默认使用 HTTPS；可显式配置 HTTP，但不接受带用户名、密码、路径或查询参数的 Endpoint。

凭据使用阿里云官方默认凭据链，支持标准环境变量、OIDC、实例角色等来源。使用环境变量时，由部署 Secret 注入 `ALIBABA_CLOUD_ACCESS_KEY_ID`、`ALIBABA_CLOUD_ACCESS_KEY_SECRET`，临时凭据再注入 `ALIBABA_CLOUD_SECURITY_TOKEN`；不要在 logging YAML 中重复配置凭据。角色临时凭据继续由凭据 SDK 自动刷新；静态环境变量 STS 需由部署方更新并重启进程。普通 logging 配置仍只在启动时读取一次。

以下字段全部可省略，仅覆盖需要调整的值：

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| topic | 空字符串 | SLS 日志主题，例如可设为 zmall-product |
| source | 当前主机名 | 在 Kubernetes 中通常为 Pod 名 |
| buffer_size_mb | 8 | 待发送记录的序列化数据容量上限，单位 MiB；不代表整个进程的内存占用 |
| batch_size | 1024 | 单批日志条数阈值，同时保留 512 KiB 批大小阈值 |
| flush_interval | 1s | 小批量日志的发送等待时间 |
| retries | 3 | 发送失败后的重试次数，0 表示不重试 |
| timeout | 3s | 单次 SLS HTTP 请求超时，不包含凭据链自身的获取耗时 |
| shutdown_timeout | 5s | 显式 Sync 或关闭时的总等待上限 |

只保留一层最多 8192 条的等待队列和一个批处理协程，缓冲容量同时覆盖队列与正在发送的批次。业务输出只转换记录并入队，不等待网络请求或凭据获取；缓冲满、单条记录超过 512 KiB 或发送最终失败时，本条 SLS 上传失败，已有控制台和文件输出继续工作。SLS 不会自动回读文件补传，重试也不提供严格的恰好一次语义。

目标配置不完整或参数格式错误会阻止初始化。网络、凭据和权限在异步发送阶段验证，初始化成功不代表云端已接收日志。发送错误通过同一 Zap 编码方式写入标准错误，module=logging，30 秒内限频；不打印原始凭据、SDK 响应或日志载荷，不经全局 klog 再次上传以避免递归。

显式 Sync 主动刷新已排队的小批次。退出时停止接收新记录，排空队列并等待批次发送和重试，最多等待 shutdown_timeout。超时或已出现上传丢失时 Close 返回错误，并输出本地诊断。关闭会立即触发小批次发送，不等待 flush_interval；超时后取消在途 HTTP 请求和重试等待。默认凭据链不提供 context 接口，已开始的凭据获取可能继续等待其自身超时，但返回后不会再发出 HTTP 请求。Fatal 同样进行有界关闭。没有 SLS 配置时，现有退出行为保持一致。

编码和签名复用阿里云官方 SDK，发送不再叠加 SDK Producer 的队列或内部重试。HTTP 408、429、5xx 与网络异常按 retries 重试，其他 HTTP 错误直接记录失败。发送失败可能存在服务端已接收但客户端未确认的情况，无法承诺恰好一次。

参考：[阿里云 PutLogs](https://help.aliyun.com/zh/sls/developer-reference/api-sls-2020-12-30-putlogs)、[阿里云默认凭据链](https://github.com/aliyun/credentials-go/blob/master/README-CN.md)。
