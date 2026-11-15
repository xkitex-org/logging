package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aliyun/aliyun-log-go-sdk"
	"github.com/aliyun/credentials-go/credentials"
	"go.uber.org/zap/zapcore"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/config"
)

const (
	maxSLSRecords    = 8192
	maxSLSBatchBytes = 512 << 10
)

// slsCore 将同一条 Zap 记录交给有界队列，不改变控制台或文件输出。
type slsCore struct {
	sender *slsSender
	fields []zapcore.Field
}

// slsRecord 保存原始日志及其占用的容量，直到发送结束才归还容量。
type slsRecord struct {
	log  *sls.Log
	size int64
}

// slsSender 仅保留一层有界队列和一个批处理协程，直接复用官方 SDK 的编码及签名。
type slsSender struct {
	settings    config.SLS
	credential  credentials.Credential
	client      *http.Client
	endpoint    *url.URL
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan slsRecord
	flush       chan chan struct{}
	done        chan struct{}
	mu          sync.RWMutex
	closed      bool
	pending     atomic.Int64
	bytes       atomic.Int64
	failed      atomic.Int64
	lastReport  atomic.Int64
	diagnostics zapcore.Core
	closeOnce   sync.Once
	closeErr    error
}

// newSLSCore 只在存在 sls 配置时创建发送器；初始化不探测 SLS，凭据获取留在工作协程。
func newSLSCore(cfg config.SLS, format string) (*slsCore, error) {
	credential, err := credentials.NewCredential(nil)
	if err != nil {
		return nil, errors.New("logging: 无法初始化 SLS 凭据链")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, errors.New("logging: SLS endpoint 无效")
	}
	endpoint.Host = cfg.Project + "." + endpoint.Host
	endpoint.Path = "/logstores/" + cfg.Logstore + "/shards/lb"
	if cfg.Source == "" {
		cfg.Source, _ = os.Hostname()
	}
	ctx, cancel := context.WithCancel(context.Background())
	sender := &slsSender{
		settings: cfg, credential: credential, endpoint: endpoint,
		client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: cfg.Timeout, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second}, Timeout: cfg.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		ctx:    ctx, cancel: cancel, queue: make(chan slsRecord, maxSLSRecords),
		flush: make(chan chan struct{}), done: make(chan struct{}),
		diagnostics: zapcore.NewCore(encoder(format), zapcore.Lock(writerSyncer{os.Stderr}), zapcore.WarnLevel),
	}
	go sender.run()
	return &slsCore{sender: sender}, nil
}

// Enabled 接受由 Kitex 完成级别过滤的全部记录，行为与本地后端一致。
func (c *slsCore) Enabled(level zapcore.Level) bool { return level >= zapcore.DebugLevel }

// With 保留 Zap 字段，派生 Core 不另建连接或发送队列。
func (c *slsCore) With(fields []zapcore.Field) zapcore.Core {
	return &slsCore{sender: c.sender, fields: append(append([]zapcore.Field(nil), c.fields...), fields...)}
}

// Check 将被接受的记录加入当前输出，交由 Write 入队。
func (c *slsCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

// Write 只转换记录并尝试入队，队列满立即归还容量，不等待网络、凭据或重试。
func (c *slsCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	s := c.sender
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("logging: SLS 发送器已关闭")
	}
	log, err := slsLog(entry, append(append([]zapcore.Field(nil), c.fields...), fields...))
	if err != nil {
		s.failed.Add(1)
		s.report("SLS 日志字段编码失败", false)
		return nil
	}
	size := int64(log.Size())
	for {
		used := s.bytes.Load()
		if size > maxSLSBatchBytes || size > (int64(s.settings.BufferSizeMB)<<20)-used {
			s.failed.Add(1)
			s.report("SLS 缓冲已满或单条日志超过 512 KiB，本条未上传", false)
			return nil
		}
		if s.bytes.CompareAndSwap(used, used+size) {
			break
		}
	}
	s.pending.Add(1)
	select {
	case s.queue <- slsRecord{log: log, size: size}:
	default:
		s.bytes.Add(-size)
		s.pending.Add(-1)
		s.failed.Add(1)
		s.report("SLS 队列已满，本条未上传", false)
	}
	return nil
}

// run 按条数、大小或时间发送；刷新请求立即处理队列，关闭则排空后退出。
func (s *slsSender) run() {
	defer close(s.done)
	defer s.client.CloseIdleConnections()
	ticker := time.NewTicker(s.settings.FlushInterval)
	defer ticker.Stop()
	var batch []slsRecord
	var size int64
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.send(batch); err != nil {
			s.failed.Add(int64(len(batch)))
			s.report(err.Error(), false)
		}
		s.bytes.Add(-size)
		s.pending.Add(-int64(len(batch)))
		batch, size = nil, 0
	}
	accept := func(record slsRecord) {
		if size+record.size > maxSLSBatchBytes {
			flush()
		}
		batch, size = append(batch, record), size+record.size
		if len(batch) >= s.settings.BatchSize || size >= maxSLSBatchBytes {
			flush()
		}
	}
	for {
		select {
		case record, ok := <-s.queue:
			if !ok {
				flush()
				return
			}
			accept(record)
		case <-ticker.C:
			flush()
		case ack := <-s.flush:
			// Logger.Sync 持有写锁，业务此时不会继续入队；上限避免直接使用 Core 时持续写入拖住刷新。
			count := len(s.queue)
			for i := 0; i < count; i++ {
				accept(<-s.queue)
			}
			flush()
			close(ack)
		}
	}
}

// send 使用官方日志编码与签名发送批次，重试只存在于这一层，取消覆盖重试等待和在途 HTTP。
func (s *slsSender) send(records []slsRecord) error {
	group := &sls.LogGroup{Topic: &s.settings.Topic, Source: &s.settings.Source}
	for _, record := range records {
		group.Logs = append(group.Logs, record.log)
	}
	body, err := group.Marshal()
	if err != nil {
		return errors.New("SLS 日志批次编码失败")
	}
	var last error
	for attempt := 0; attempt <= s.settings.Retries; attempt++ {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			timer := time.NewTimer(min(100*time.Millisecond*time.Duration(1<<min(attempt-1, 4)), time.Second))
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return s.ctx.Err()
			case <-timer.C:
			}
		}
		if err := s.ctx.Err(); err != nil {
			return err
		}
		credential, err := s.credential.GetCredential()
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		if err != nil || credential == nil || credential.AccessKeyId == nil || credential.AccessKeySecret == nil || *credential.AccessKeyId == "" || *credential.AccessKeySecret == "" {
			last = errors.New("SLS 凭据获取失败，请检查阿里云凭据链配置")
			continue
		}
		headers := map[string]string{
			sls.HTTPHeaderHost:        s.endpoint.Host,
			sls.HTTPHeaderDate:        time.Now().UTC().Format(http.TimeFormat),
			sls.HTTPHeaderAPIVersion:  "0.6.0",
			sls.HTTPHeaderBodyRawSize: strconv.Itoa(len(body)),
			sls.HTTPHeaderContentType: "application/x-protobuf",
		}
		if credential.SecurityToken != nil && *credential.SecurityToken != "" {
			headers[sls.HTTPHeaderAcsSecurityToken] = *credential.SecurityToken
		}
		if err := sls.NewSignerV1(*credential.AccessKeyId, *credential.AccessKeySecret).Sign(http.MethodPost, s.endpoint.RequestURI(), headers, body); err != nil {
			return errors.New("SLS 请求签名失败")
		}
		request, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return errors.New("SLS 请求创建失败")
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := s.client.Do(request)
		if err != nil {
			last = errors.New("SLS 网络请求失败")
			continue
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK && readErr == nil {
			return nil
		}
		last = fmt.Errorf("SLS 请求失败，HTTP 状态码=[%d]", response.StatusCode)
		if response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout && response.StatusCode != http.StatusTooManyRequests && readErr == nil {
			return last
		}
	}
	return last
}

// Sync 主动刷新已排队的小批次，等待时间包含排队、发送及失败重试。
func (c *slsCore) Sync() error {
	s := c.sender
	timer := time.NewTimer(s.settings.ShutdownTimeout)
	defer timer.Stop()
	ack := make(chan struct{})
	select {
	case s.flush <- ack:
	case <-s.done:
		return s.result()
	case <-timer.C:
		return errors.New("logging: 等待 SLS 日志刷新超时")
	}
	select {
	case <-ack:
		return s.result()
	case <-s.done:
		return s.result()
	case <-timer.C:
		return errors.New("logging: 等待 SLS 日志刷新超时")
	}
}

// result 报告累计上传丢失，成功退出不等于曾丢失的日志被补传。
func (s *slsSender) result() error {
	if s.failed.Load() > 0 {
		return errors.New("logging: 存在未成功上传的 SLS 日志")
	}
	return nil
}

// close 原子停止入队并立即排空；总等待到期后取消在途请求，不重复等待两个超时。
func (s *slsSender) close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()
		defer s.cancel()
		timer := time.NewTimer(s.settings.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-s.done:
			s.closeErr = s.result()
		case <-timer.C:
			s.cancel()
			s.closeErr = errors.New("logging: SLS 退出等待超时，已取消发送，仍有日志未确认")
		}
		if s.closeErr != nil {
			s.report(s.closeErr.Error(), true)
		}
	})
	return s.closeErr
}

// report 将限频诊断写到本地标准错误，不经全局 klog 重入日志锁或递归上传。
func (s *slsSender) report(message string, force bool) {
	now := time.Now()
	if !force {
		previous := s.lastReport.Load()
		if now.UnixNano()-previous < int64(30*time.Second) || !s.lastReport.CompareAndSwap(previous, now.UnixNano()) {
			return
		}
	}
	_ = s.diagnostics.Write(zapcore.Entry{Level: zapcore.ErrorLevel, Time: now, LoggerName: "logging", Message: fmt.Sprintf("logging: %s，累计未上传=[%d] 待确认=[%d]", message, s.failed.Load(), s.pending.Load())}, nil)
}

// slsLog 保留日志原始时间、级别、模块、调用位置及附加字段。
func slsLog(entry zapcore.Entry, fields []zapcore.Field) (*sls.Log, error) {
	seconds, nanos := uint32(entry.Time.Unix()), uint32(entry.Time.Nanosecond())
	log := &sls.Log{Time: &seconds, TimeNs: &nanos, Contents: []*sls.LogContent{
		slsContent("level", entry.Level.String()), slsContent("ts", entry.Time.Format("2006-01-02T15:04:05.000-0700")),
		slsContent("module", entry.LoggerName), slsContent("msg", entry.Message),
	}}
	if entry.Caller.Defined {
		log.Contents = append(log.Contents, slsContent("caller", entry.Caller.TrimmedPath()))
	}
	if len(fields) > 0 {
		encoded := zapcore.NewMapObjectEncoder()
		for _, field := range fields {
			field.AddTo(encoded)
		}
		for key, value := range encoded.Fields {
			text, ok := value.(string)
			if !ok {
				body, err := json.Marshal(value)
				if err != nil {
					return nil, err
				}
				text = string(body)
			}
			log.Contents = append(log.Contents, slsContent(key, text))
		}
	}
	return log, nil
}

// slsContent 创建单个 SLS 字段，不拼接或解析日志消息。
func slsContent(key, value string) *sls.LogContent { return &sls.LogContent{Key: &key, Value: &value} }
