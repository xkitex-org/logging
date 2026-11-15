package adapter

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aliyun/aliyun-log-go-sdk"
	"github.com/aliyun/credentials-go/credentials"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"go.chaoxizhongzhi.com/xkitex/logging/internal/config"
)

// slsTestTransport 仅在内存中模拟 HTTP 响应，不创建任何外部连接。
type slsTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip 执行测试提供的内存响应逻辑。
func (f slsTestTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// slsTestResponse 创建无敏感内容的模拟响应。
func slsTestResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
}

// slsTestCore 使用假凭据与内存传输验证真实发送流程，不增加生产代码的测试注入接口。
func slsTestCore(t *testing.T, overrides string, transport slsTestTransport) *slsCore {
	t.Helper()
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "fake-access-key")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "fake-secret")
	t.Setenv("ALIBABA_CLOUD_SECURITY_TOKEN", "")
	cfg, err := config.Parse([]byte("sls: {endpoint: https://sls.invalid, project: example, logstore: application, flush_interval: 1m, " + overrides + "}"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := newSLSCore(*cfg.SLS, "json")
	if err != nil {
		t.Fatal(err)
	}
	core.sender.client.Transport = transport
	core.sender.diagnostics = zapcore.NewNopCore()
	t.Cleanup(func() {
		_ = core.sender.close()
		select {
		case <-core.sender.done:
		case <-time.After(time.Second):
			t.Error("SLS worker did not stop")
		}
	})
	return core
}

// slsWrite 写入一条日志，保持业务调用元信息。
func slsWrite(t *testing.T, core *slsCore, message string) {
	t.Helper()
	if err := core.Write(zapcore.Entry{Level: zapcore.InfoLevel, Time: time.Now(), LoggerName: "product", Message: message}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestSLSRecordFields 验证原始时间、业务模块、消息和附加字段，不创建网络客户端。
func TestSLSRecordFields(t *testing.T) {
	now := time.Unix(1800000000, 123456789)
	log, err := slsLog(zapcore.Entry{Level: zapcore.ErrorLevel, Time: now, LoggerName: "product", Message: "示例错误\n第二行", Caller: zapcore.NewEntryCaller(0, "/src/service/user.go", 42, true)}, []zapcore.Field{zap.String("orderId", "ORD123"), zap.Int("attempt", 2)})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, content := range log.Contents {
		got[content.GetKey()] = content.GetValue()
	}
	if log.GetTime() != uint32(now.Unix()) || log.GetTimeNs() != 123456789 || got["module"] != "product" || got["caller"] != "service/user.go:42" || got["msg"] != "示例错误\n第二行" || got["orderId"] != "ORD123" || got["attempt"] != "2" || got["level"] != "error" {
		t.Fatalf("lost SLS metadata: %+v", got)
	}
}

// TestSLSSyncFlushesSmallBatch 验证一分钟发送间隔下 Sync 仍立即发送小批次，并检查真实协议编码。
func TestSLSSyncFlushesSmallBatch(t *testing.T) {
	var calls atomic.Int64
	core := slsTestCore(t, "shutdown_timeout: 1s", func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		var group sls.LogGroup
		if err := group.Unmarshal(body); err != nil {
			t.Error(err)
		}
		if request.URL.Host != "example.sls.invalid" || request.URL.Path != "/logstores/application/shards/lb" || request.Header.Get("x-log-bodyrawsize") != strconv.Itoa(len(body)) || !strings.HasPrefix(request.Header.Get("Authorization"), "LOG fake-access-key:") || request.Header.Get("Content-MD5") == "" || len(group.Logs) != 1 {
			t.Error("invalid signed SLS protocol")
		}
		calls.Add(1)
		return slsTestResponse(200), nil
	})
	slsWrite(t, core, "one-record")
	if err := core.Sync(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || core.sender.pending.Load() != 0 || core.sender.bytes.Load() != 0 {
		t.Fatal("Sync did not flush or release capacity")
	}
	if err := core.sender.close(); err != nil {
		t.Fatal(err)
	}
	if err := core.Sync(); err != nil {
		t.Fatal(err)
	}
}

// TestSLSRetryPolicy 验证临时错误重试、零重试和永久错误，不存在 SDK 内部隐含重试。
func TestSLSRetryPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		retries  int
		statuses []int
		want     int
		failed   bool
	}{
		{"transient", 2, []int{503, 429, 200}, 3, false},
		{"zero", 0, []int{503}, 1, true},
		{"forbidden", 3, []int{403}, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			core := slsTestCore(t, "retries: "+strconv.Itoa(test.retries), func(_ *http.Request) (*http.Response, error) {
				i := int(calls.Add(1)) - 1
				return slsTestResponse(test.statuses[min(i, len(test.statuses)-1)]), nil
			})
			slsWrite(t, core, test.name)
			if err := core.Sync(); (err != nil) != test.failed {
				t.Fatalf("unexpected flush result: %v", err)
			}
			if calls.Load() != int64(test.want) || core.sender.pending.Load() != 0 || core.sender.bytes.Load() != 0 {
				t.Fatal("retry count or capacity mismatch")
			}
		})
	}
}

// TestSLSQueueFullAndConcurrentClose 验证发送阻塞期间有界入队不等待网络，关闭能排空且不会发送到已关闭通道。
func TestSLSQueueFullAndConcurrentClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	core := slsTestCore(t, "batch_size: 1024", func(_ *http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-release
		return slsTestResponse(200), nil
	})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	for i := 0; i < 1024; i++ {
		slsWrite(t, core, "in-flight")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	for i := 0; i < maxSLSRecords; i++ {
		slsWrite(t, core, "queued")
	}
	before := core.sender.bytes.Load()
	slsWrite(t, core, "full")
	if core.sender.failed.Load() != 1 || core.sender.bytes.Load() != before || len(core.sender.queue) != maxSLSRecords {
		t.Fatal("full queue blocked or retained capacity")
	}
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; i < 50; i++ {
			_ = core.Write(zapcore.Entry{Time: time.Now(), Message: "racing-close"}, nil)
		}
	}()
	closed := make(chan error, 1)
	go func() { closed <- core.sender.close() }()
	releaseAll()
	writers.Wait()
	if err := <-closed; err == nil {
		t.Fatal("queue loss was hidden on close")
	}
	if core.sender.pending.Load() != 0 || core.sender.bytes.Load() != 0 {
		t.Fatal("close left outstanding records")
	}
}

// TestSLSCloseFlushesAndCancels 验证最后一批立即发送、关闭幂等以及超时确实取消在途 HTTP。
func TestSLSCloseFlushesAndCancels(t *testing.T) {
	t.Run("final batch", func(t *testing.T) {
		var calls atomic.Int64
		core := slsTestCore(t, "shutdown_timeout: 1s", func(_ *http.Request) (*http.Response, error) { calls.Add(1); return slsTestResponse(200), nil })
		slsWrite(t, core, "final")
		if err := core.sender.close(); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 || core.sender.pending.Load() != 0 {
			t.Fatal("last batch was not confirmed")
		}
		if err := core.sender.close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancel in-flight", func(t *testing.T) {
		entered, cancelled := make(chan struct{}), make(chan struct{})
		core := slsTestCore(t, "batch_size: 1, shutdown_timeout: 30ms", func(request *http.Request) (*http.Response, error) {
			close(entered)
			<-request.Context().Done()
			close(cancelled)
			return nil, request.Context().Err()
		})
		slsWrite(t, core, "blocked")
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not start")
		}
		err := core.sender.close()
		if err == nil || core.sender.close() != err {
			t.Fatal("timeout or idempotent result missing")
		}
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("HTTP request was not cancelled")
		}
		select {
		case <-core.sender.done:
		case <-time.After(time.Second):
			t.Fatal("sender kept retrying after cancellation")
		}
		if core.sender.pending.Load() != 0 || core.sender.bytes.Load() != 0 || core.sender.failed.Load() != 1 {
			t.Fatal("cancel did not release capacity")
		}
	})
}

// slsTestCredential 只在测试中阻塞凭据获取，用于确认关闭后不会补发 HTTP 请求。
type slsTestCredential struct {
	credentials.Credential
	entered chan struct{}
	release chan struct{}
}

// GetCredential 在测试释放前等待，随后返回假凭据。
func (p *slsTestCredential) GetCredential() (*credentials.CredentialModel, error) {
	close(p.entered)
	<-p.release
	id, secret := "fake-access-key", "fake-secret"
	return &credentials.CredentialModel{AccessKeyId: &id, AccessKeySecret: &secret}, nil
}

// TestSLSCloseDuringCredentialFetch 验证不可取消的凭据读取不会拖住 Close，返回后也不会补发请求。
func TestSLSCloseDuringCredentialFetch(t *testing.T) {
	var calls atomic.Int64
	core := slsTestCore(t, "batch_size: 1, shutdown_timeout: 30ms", func(_ *http.Request) (*http.Response, error) { calls.Add(1); return slsTestResponse(200), nil })
	provider := &slsTestCredential{entered: make(chan struct{}), release: make(chan struct{})}
	core.sender.credential = provider
	var once sync.Once
	release := func() { once.Do(func() { close(provider.release) }) }
	defer release()
	slsWrite(t, core, "credential-wait")
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("credential fetch did not start")
	}
	if err := core.sender.close(); err == nil {
		t.Fatal("credential wait did not time out")
	}
	release()
	select {
	case <-core.sender.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after credential fetch")
	}
	if calls.Load() != 0 || core.sender.pending.Load() != 0 || core.sender.bytes.Load() != 0 {
		t.Fatal("cancelled credential fetch submitted a late request")
	}
}

// TestSLSFullBufferRejectsWithoutSending 验证字节容量和超大日志在入队前被拒绝。
func TestSLSFullBufferRejectsWithoutSending(t *testing.T) {
	sender := &slsSender{settings: config.SLS{BufferSizeMB: 1}, diagnostics: zapcore.NewNopCore(), queue: make(chan slsRecord, 1)}
	sender.bytes.Store(1 << 20)
	core := &slsCore{sender: sender}
	slsWrite(t, core, "full-bytes")
	sender.bytes.Store(0)
	slsWrite(t, core, strings.Repeat("x", 513<<10))
	if sender.failed.Load() != 2 || sender.pending.Load() != 0 || len(sender.queue) != 0 || sender.bytes.Load() != 0 {
		t.Fatal("capacity rejection leaked a reservation")
	}
}

// TestSLSFailurePreservesLocalFile 验证远端失败后，本地文件仍完整落盘并关闭。
func TestSLSFailurePreservesLocalFile(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "fake-access-key")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "fake-secret")
	path := filepath.Join(t.TempDir(), "app.log")
	t.Setenv("X_LOGGING_FILE", path)
	logger := New()
	if err := logger.ConfigureLogging([]byte("console: false\nfile: {enabled: true}\nsls: {endpoint: https://sls.invalid, project: example, logstore: application, retries: 0}")); err != nil {
		t.Fatal(err)
	}
	logger.backend.sls.client.Transport = slsTestTransport(func(_ *http.Request) (*http.Response, error) { return slsTestResponse(403), nil })
	logger.backend.sls.diagnostics = zapcore.NewNopCore()
	logger.Info("keep-local-record")
	if err := logger.Close(); err == nil {
		t.Fatal("SLS failure was not reported")
	}
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "keep-local-record") {
		t.Fatal("local record was lost")
	}
}
