package daemon

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/encoding/protojson"

	pairv1 "github.com/FooFishes/pairwise/gen/pair/v1"
	"github.com/FooFishes/pairwise/gen/pair/v1/pairv1connect"
	"github.com/FooFishes/pairwise/web"
)

// tokenTTL 是一次性界面访问令牌的有效期.
const tokenTTL = 10 * time.Minute

// heartbeat 是 SSE 心跳间隔, 防止空闲连接被中间环节断开.
const heartbeat = 15 * time.Second

// newHandler 组装常驻进程的全部 HTTP 端点. Unix Socket 与 TCP 两个监听共用它,
// 只有来自 TCP 监听的请求经过 guard 校验.
func newHandler(svc Service, port int) http.Handler {
	rpc := connect.NewServer(keepErrorMessage)
	pairv1connect.RegisterPairServiceHandler(rpc, svc)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, rpc)
	g := &guard{
		port:     port,
		cookie:   fmt.Sprintf("pair_session_%d", port),
		tokens:   map[string]time.Time{},
		sessions: map[string]bool{},
	}
	mux.HandleFunc("GET "+EventsPath, events(svc))
	mux.HandleFunc("POST "+TokenPath, g.issue)
	// 不带方法限定: "GET /" 与 Connect 挂载的 "/pair.v1.PairService/" 互不包含, 注册时会冲突.
	mux.Handle("/", ui(web.FS()))
	return g.wrap(mux)
}

// keepErrorMessage 把业务实现返回的普通错误 (I/O、git 调用失败等) 转为带原因的 CodeInternal 错误并记入日志.
// Connect 会抹去非 *connect.Error 的错误信息, 否则 CLI 只能输出 "unknown", 无从排查.
func keepErrorMessage(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		err := next(ctx, spec, stream)
		if err == nil || errors.As(err, new(*connect.Error)) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		slog.Error("处理请求失败", "procedure", spec.Procedure, "err", err)
		return connect.NewError(connect.CodeInternal, "常驻进程内部错误: "+err.Error()).WithCause(err)
	}
}

// guard 校验界面侧请求: Host 与 Origin 必须是本机地址, 并持有一次性令牌换来的会话 Cookie.
// 令牌与会话只存内存, 常驻进程重启后全部失效.
type guard struct {
	port int
	// cookie 是会话 Cookie 名. 浏览器不按端口隔离 Cookie, 名字带上端口以免多个工作树的会话互相覆盖.
	cookie string

	mu       sync.Mutex
	tokens   map[string]time.Time
	sessions map[string]bool
}

func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(http.LocalAddrContextKey).(net.Addr).Network() == "unix" {
			next.ServeHTTP(w, r)
			return
		}
		if !g.local(r.Host) {
			http.Error(w, "拒绝访问: Host 不是本机地址", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !g.local(strings.TrimPrefix(origin, "http://")) {
			http.Error(w, "拒绝访问: 跨站请求", http.StatusForbidden)
			return
		}
		if r.URL.Path == TokenPath {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && g.redeem(r.URL.Query().Get("token")) {
			g.login(w, r)
			return
		}
		if !g.authorized(r) {
			http.Error(w, "未授权: 请打开 `pair serve` 输出的地址", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// local 报告 host 是否为 127.0.0.1:<port> 或 localhost:<port>.
func (g *guard) local(host string) bool {
	port := strconv.Itoa(g.port)
	return host == "127.0.0.1:"+port || host == "localhost:"+port
}

// issue 签发一次性令牌, 返回带令牌的界面地址.
func (g *guard) issue(w http.ResponseWriter, _ *http.Request) {
	token := rand.Text()
	g.mu.Lock()
	maps.DeleteFunc(g.tokens, func(_ string, at time.Time) bool { return time.Since(at) >= tokenTTL })
	g.tokens[token] = time.Now()
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": fmt.Sprintf("http://127.0.0.1:%d/?token=%s", g.port, token),
	})
}

// redeem 作废令牌并报告它在作废前是否有效.
func (g *guard) redeem(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.tokens[token]
	delete(g.tokens, token)
	return ok && time.Since(at) < tokenTTL
}

// login 签发会话 Cookie 并重定向到去掉令牌的地址, 令牌不留在地址栏与历史记录中.
func (g *guard) login(w http.ResponseWriter, r *http.Request) {
	session := rand.Text()
	g.mu.Lock()
	g.sessions[session] = true
	g.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     g.cookie,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	q := r.URL.Query()
	q.Del("token")
	// path.Clean 去掉开头多余的斜杠, 避免 //host 形式被当作跨站地址.
	target := url.URL{Path: path.Clean(r.URL.Path), RawQuery: q.Encode()}
	http.Redirect(w, r, target.RequestURI(), http.StatusFound)
}

func (g *guard) authorized(r *http.Request) bool {
	c, err := r.Cookie(g.cookie)
	if err != nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[c.Value]
}

// events 以 SSE 推送序号大于起点的事件. 先订阅再补发历史, 两步之间追加的事件不会丢失,
// 实时事件中已补发过的序号被跳过. 起点取 Last-Event-ID 请求头, 其次 ?since=.
func events(svc Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		last, err := strconv.ParseInt(cmp.Or(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("since"), "0"), 10, 64)
		if err != nil {
			http.Error(w, "事件起点必须是整数序号", http.StatusBadRequest)
			return
		}
		live, cancel := svc.Subscribe()
		defer cancel()
		backlog, err := svc.EventsSince(last)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		send := func(ev *pairv1.Event) error {
			if ev.Seq <= last {
				return nil
			}
			data, err := protojson.Marshal(ev)
			if err != nil {
				return err
			}
			last = ev.Seq
			_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, data)
			return err
		}
		for _, ev := range backlog {
			if send(ev) != nil {
				return
			}
		}
		rc := http.NewResponseController(w)
		tick := time.NewTicker(heartbeat)
		defer tick.Stop()
		for {
			if rc.Flush() != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-live:
				// 订阅通道关闭时结束响应, 客户端凭最后序号重连.
				if !ok || send(ev) != nil {
					return
				}
			case <-tick.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
			}
		}
	}
}

// ui 提供内嵌的工作台前端. 不是文件的路径回退到 index.html, 交给前端路由.
func ui(fsys fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			http.Error(w, "工作台前端尚未构建", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if fi, err := fs.Stat(fsys, name); err != nil || !fi.Mode().IsRegular() {
			name = "index.html"
		}
		http.ServeFileFS(w, r, fsys, name)
	}
}
