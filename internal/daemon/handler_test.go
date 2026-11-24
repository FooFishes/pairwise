package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
)

// stubService 只实现传输层测试用到的方法, 业务接口一律返回未实现.
type stubService struct {
	pairv1connect.UnimplementedPairServiceHandler
	backlog            []*pairv1.Event
	live               chan *pairv1.Event
	reloaded, branched chan struct{}
	done               chan struct{}
	closed             bool
}

func (s *stubService) Ping(context.Context, *pairv1.PingRequest) (*pairv1.PingResponse, error) {
	return &pairv1.PingResponse{}, nil
}

// Status 模拟业务实现返回的普通错误 (非 *connect.Error).
func (s *stubService) Status(context.Context, *pairv1.StatusRequest) (*pairv1.StatusResponse, error) {
	return nil, errors.New("磁盘已满")
}

func (s *stubService) Subscribe() (<-chan *pairv1.Event, func()) { return s.live, func() {} }

func (s *stubService) EventsSince(seq int64) ([]*pairv1.Event, error) {
	return slices.DeleteFunc(slices.Clone(s.backlog), func(ev *pairv1.Event) bool { return ev.Seq <= seq }), nil
}

func (s *stubService) Reload(context.Context)        { notify(s.reloaded) }
func (s *stubService) BranchChanged(context.Context) { notify(s.branched) }
func (s *stubService) SetUIURL(string)               {}
func (s *stubService) Done() <-chan struct{}         { return s.done }
func (s *stubService) Close() error                  { s.closed = true; return nil }

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// serve 像 Run 一样在真实的 TCP 与 Unix Socket 监听上运行 newHandler,
// 返回 TCP 侧地址与经 Unix Socket 访问的客户端.
func serve(t *testing.T, svc Service) (string, *http.Client) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Socket 路径受系统长度上限约束, 不用很深的 t.TempDir().
	dir, err := os.MkdirTemp("", "pair")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	unix, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: newHandler(svc, port)}
	go func() { _ = srv.Serve(tcp) }()
	go func() { _ = srv.Serve(unix) }()
	t.Cleanup(func() { srv.Close() })
	return fmt.Sprintf("http://127.0.0.1:%d", port), unixClient(sock)
}

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func do(t *testing.T, c *http.Client, method, url, host, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestGuard(t *testing.T) {
	base, unix := serve(t, &stubService{})
	port := strings.TrimPrefix(base, "http://127.0.0.1:")
	ping := base + pairv1connect.PairServicePingProcedure

	res, err := unix.Post("http://pair"+TokenPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var token struct{ URL string }
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if !strings.HasPrefix(token.URL, base+"/?token=") {
		t.Fatalf("令牌地址 = %q", token.URL)
	}

	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res = do(t, browser, http.MethodGet, token.URL, "", "", nil)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("令牌换 Cookie: %s, Location %q", res.Status, res.Header.Get("Location"))
	}
	if c := res.Header.Get("Set-Cookie"); !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Strict") {
		t.Fatalf("Set-Cookie = %q", c)
	}
	session := res.Cookies()[0]

	tests := []struct {
		name         string
		client       *http.Client
		method, url  string
		host, origin string
		cookie       *http.Cookie
		want         int
	}{
		{"Unix Socket 不校验", unix, http.MethodPost, "http://pair" + pairv1connect.PairServicePingProcedure, "", "", nil, http.StatusOK},
		{"会话 Cookie", browser, http.MethodPost, ping, "", "", session, http.StatusOK},
		{"localhost 同源", browser, http.MethodPost, ping, "localhost:" + port, "http://localhost:" + port, session, http.StatusOK},
		{"127.0.0.1 同源", browser, http.MethodPost, ping, "", base, session, http.StatusOK},
		{"无 Cookie", browser, http.MethodPost, ping, "", "", nil, http.StatusUnauthorized},
		{"伪造 Cookie", browser, http.MethodPost, ping, "", "", &http.Cookie{Name: session.Name, Value: "forged"}, http.StatusUnauthorized},
		{"令牌只能用一次", browser, http.MethodGet, token.URL, "", "", nil, http.StatusUnauthorized},
		{"伪造令牌", browser, http.MethodGet, base + "/?token=forged", "", "", nil, http.StatusUnauthorized},
		{"非本机 Host", browser, http.MethodPost, ping, "evil.example:" + port, "", session, http.StatusForbidden},
		{"其他端口 Host", browser, http.MethodPost, ping, "127.0.0.1:1", "", session, http.StatusForbidden},
		{"跨站 Origin", browser, http.MethodPost, ping, "", "http://evil.example", session, http.StatusForbidden},
		{"TCP 侧不能申请令牌", browser, http.MethodPost, base + TokenPath, "", "", session, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := do(t, tt.client, tt.method, tt.url, tt.host, tt.origin, tt.cookie).StatusCode; got != tt.want {
				t.Errorf("状态码 = %d, 期望 %d", got, tt.want)
			}
		})
	}
}

func TestEvents(t *testing.T) {
	backlog := []*pairv1.Event{{Seq: 1}, {Seq: 2}, {Seq: 3}}
	tests := []struct {
		name   string
		query  string
		header string
		want   []string
	}{
		{"从头补发", "", "", []string{"1", "2", "3", "4"}},
		{"Last-Event-ID", "", "1", []string{"2", "3", "4"}},
		{"since 参数", "?since=2", "", []string{"3", "4"}},
		{"请求头优先", "?since=0", "3", []string{"4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 实时事件与补发重叠的序号 3 只推送一次; 通道关闭后响应结束.
			live := make(chan *pairv1.Event, 2)
			live <- &pairv1.Event{Seq: 3}
			live <- &pairv1.Event{Seq: 4, Type: "doc_changed"}
			close(live)
			_, unix := serve(t, &stubService{backlog: backlog, live: live})
			req, err := http.NewRequest(http.MethodGet, "http://pair"+EventsPath+tt.query, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tt.header != "" {
				req.Header.Set("Last-Event-ID", tt.header)
			}
			res, err := unix.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, m := range regexp.MustCompile(`(?m)^id: (\d+)\ndata: \{.*\}\n\n`).FindAllStringSubmatch(string(body), -1) {
				ids = append(ids, m[1])
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("推送序号 = %v, 期望 %v\n%s", ids, tt.want, body)
			}
		})
	}
}

// TestKeepErrorMessage 验证普通错误的原因传到 CLI, 而不是被 Connect 抹成 "unknown".
func TestKeepErrorMessage(t *testing.T) {
	_, unix := serve(t, &stubService{})
	c := pairv1connect.NewPairServiceClient(connect.NewClient(connecthttp.NewTransport(unix, "http://pair")))
	_, err := c.Status(context.Background(), &pairv1.StatusRequest{})
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "常驻进程内部错误: 磁盘已满") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.Drain(context.Background(), &pairv1.DrainRequest{}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("业务错误码被改写: %v", err)
	}
}
