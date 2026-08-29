// This file is package security (white-box), not security_test, for
// one narrow reason: every httptest.Server in these tests binds to
// loopback, which isPublicIP correctly, deliberately, always rejects
// in production. allowLoopbackForTest patches the package's internal
// isPublicIPFunc indirection so loopback specifically is treated as
// "the public origin our test server stands in for," while every
// other rule (real private ranges, localhost-by-name, link-local,
// ...) still runs through the real, unmodified isPublicIP — so a test
// asserting a private-IP redirect target is rejected is still
// genuinely exercising the real check, not a weakened one.
// urlvalidator_test.go (package security_test) already covers
// isPublicIP itself black-box; nothing here duplicates that.
package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeResolver is a Resolver returning fixed, per-host answers and
// recording how many times each host was looked up — the second part
// is what TestFetch_DNSRebinding_OnlyResolvesOncePerHop needs.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string]net.IP
	calls   map[string]int
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{answers: map[string]net.IP{}, calls: map[string]int{}}
}

func (r *fakeResolver) set(host string, ip net.IP) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[host] = ip
}

func (r *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[host]++
	ip, ok := r.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []net.IPAddr{{IP: ip}}, nil
}

func (r *fakeResolver) callCount(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

// allowLoopbackForTest lets a real httptest.Server (always bound to
// 127.0.0.1) stand in for "some public origin" for the duration of one
// test — see the file doc for why this is the only thing it relaxes.
func allowLoopbackForTest(t *testing.T) {
	t.Helper()
	original := isPublicIPFunc
	isPublicIPFunc = func(ip net.IP) bool {
		if ip.IsLoopback() {
			return true
		}
		return original(ip)
	}
	t.Cleanup(func() { isPublicIPFunc = original })
}

// serverPort extracts the port httptest bound srv to.
func serverPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	return port
}

// newFetcherAndResolver builds a SafeFetcher backed by a fakeResolver,
// with loopback allowed so it can reach real httptest servers.
func newFetcherAndResolver(t *testing.T, cfg Config) (*SafeFetcher, *fakeResolver) {
	t.Helper()
	allowLoopbackForTest(t)
	resolver := newFakeResolver()
	validator := NewValidator(WithResolver(resolver))
	return NewSafeFetcher(validator, cfg), resolver
}

func TestFetch_NormalURL_Allowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("video-bytes"))
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("cdn.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://cdn.example:%s/video.mp4", serverPort(t, srv))
	resp, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(resp.Body) != "video-bytes" {
		t.Errorf("Body = %q, want %q", resp.Body, "video-bytes")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resp.FinalURL != url {
		t.Errorf("FinalURL = %q, want %q (no redirect happened)", resp.FinalURL, url)
	}
}

func TestFetch_PrivateIP_Blocked(t *testing.T) {
	fetcher, _ := newFetcherAndResolver(t, Config{})
	_, err := fetcher.Fetch(context.Background(), "http://10.0.0.5/video.mp4", FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) {
		t.Fatalf("Fetch() error = %v, want a *FetchError", err)
	}
	if fetchErr.Kind != FetchErrorBlocked {
		t.Errorf("Kind = %v, want FetchErrorBlocked", fetchErr.Kind)
	}
	if !fetchErr.IsPermanent() {
		t.Error("IsPermanent() = false, want true — a blocked URL will never become fetchable by retrying")
	}
}

func TestFetch_DNSResolvesToPrivateIP_Blocked(t *testing.T) {
	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("evil.example", net.ParseIP("192.168.1.1"))

	_, err := fetcher.Fetch(context.Background(), "https://evil.example/video.mp4", FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorBlocked {
		t.Fatalf("Fetch() error = %v, want FetchErrorBlocked", err)
	}
}

func TestFetch_ValidRedirect_Allowed(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	port := serverPort(t, srv) // available before Start(), since the listener already exists

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://step2.example:%s/final", port), http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("final-bytes"))
	})
	srv.Start()
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("step1.example", net.ParseIP("127.0.0.1"))
	resolver.set("step2.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://step1.example:%s/start", port)
	resp, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if string(resp.Body) != "final-bytes" {
		t.Errorf("Body = %q, want %q", resp.Body, "final-bytes")
	}
	wantFinal := fmt.Sprintf("http://step2.example:%s/final", port)
	if resp.FinalURL != wantFinal {
		t.Errorf("FinalURL = %q, want %q", resp.FinalURL, wantFinal)
	}
}

func TestFetch_RedirectToLocalhost_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost/internal", http.StatusFound)
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("start.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://start.example:%s/go", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorBlocked {
		t.Fatalf("Fetch() error = %v, want FetchErrorBlocked", err)
	}
}

func TestFetch_RedirectToPrivateIP_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil-internal.example/internal", http.StatusFound)
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("start.example", net.ParseIP("127.0.0.1"))
	resolver.set("evil-internal.example", net.ParseIP("10.0.0.9"))

	url := fmt.Sprintf("http://start.example:%s/go", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorBlocked {
		t.Fatalf("Fetch() error = %v, want FetchErrorBlocked", err)
	}
}

func TestFetch_RedirectChainExceedsLimit_Blocked(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	port := serverPort(t, srv)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hop, _ := strconv.Atoi(r.URL.Query().Get("hop"))
		http.Redirect(w, r, fmt.Sprintf("http://chain.example:%s/next?hop=%d", port, hop+1), http.StatusFound)
	})
	srv.Start()
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{MaxRedirects: 2})
	resolver.set("chain.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://chain.example:%s/next?hop=0", port)
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorTooManyRedirects {
		t.Fatalf("Fetch() error = %v, want FetchErrorTooManyRedirects", err)
	}
}

func TestFetch_ResponseExceedsMaxBytes_ContentLengthFastFail(t *testing.T) {
	body := make([]byte, 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("big.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://big.example:%s/big.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorResponseTooLarge {
		t.Fatalf("Fetch() error = %v, want FetchErrorResponseTooLarge", err)
	}
}

// TestFetch_ResponseExceedsMaxBytes_NoContentLength proves the size
// limit is enforced against actual bytes read, not just a
// Content-Length header a server can omit or lie about — the response
// here is chunked (Flush before the total size is known), so Go's
// server never sends a Content-Length at all.
func TestFetch_ResponseExceedsMaxBytes_NoContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		chunk := make([]byte, 512)
		for i := 0; i < 4; i++ { // 2048 bytes total, well over the 1024 limit below
			w.Write(chunk)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("big.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://big.example:%s/big.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorResponseTooLarge {
		t.Fatalf("Fetch() error = %v, want FetchErrorResponseTooLarge (no Content-Length was sent)", err)
	}
}

func TestFetch_SlowResponse_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte("too-late"))
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{RequestTimeout: 50 * time.Millisecond})
	resolver.set("slow.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://slow.example:%s/video.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorTimeout {
		t.Fatalf("Fetch() error = %v, want FetchErrorTimeout", err)
	}
}

func TestFetch_InvalidContentType_Blocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>not a video</html>"))
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("wrong.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://wrong.example:%s/video.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024, AllowedContentTypes: []string{"video/"}})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorUnacceptableContentType {
		t.Fatalf("Fetch() error = %v, want FetchErrorUnacceptableContentType", err)
	}
}

func TestFetch_ServerError5xx_ReturnsTransientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("down.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://down.example:%s/video.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorUnacceptableStatus {
		t.Fatalf("Fetch() error = %v, want FetchErrorUnacceptableStatus", err)
	}
	if fetchErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want %d", fetchErr.StatusCode, http.StatusServiceUnavailable)
	}
	if fetchErr.IsPermanent() {
		t.Error("IsPermanent() = true, want false — a 5xx is worth retrying")
	}
}

func TestFetch_ClientError4xx_ReturnsPermanentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("missing.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://missing.example:%s/video.mp4", serverPort(t, srv))
	_, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024})

	var fetchErr *FetchError
	if !asFetchError(err, &fetchErr) || fetchErr.Kind != FetchErrorUnacceptableStatus {
		t.Fatalf("Fetch() error = %v, want FetchErrorUnacceptableStatus", err)
	}
	if !fetchErr.IsPermanent() {
		t.Error("IsPermanent() = false, want true — a 4xx won't fix itself on retry")
	}
}

func TestFetch_ContextCancellation_StopsRequest(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(2 * time.Second)
		w.Write([]byte("too-late"))
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("cancel.example", net.ParseIP("127.0.0.1"))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	url := fmt.Sprintf("http://cancel.example:%s/video.mp4", serverPort(t, srv))
	start := time.Now()
	_, err := fetcher.Fetch(ctx, url, FetchOptions{MaxBytes: 1024})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Fetch() error = nil, want an error after the context was canceled")
	}
	if elapsed > time.Second {
		t.Errorf("Fetch() took %v to return after cancellation, want well under the server's 2s sleep", elapsed)
	}
}

// TestFetch_DNSRebinding_OnlyResolvesOncePerHop proves the actual
// connection can't be steered somewhere the validator never approved:
// the resolver is consulted exactly once for the hop's host, and
// whatever it returns is what gets dialed — there's no second,
// independent resolution for the real connection to disagree with.
func TestFetch_DNSRebinding_OnlyResolvesOncePerHop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	fetcher, resolver := newFetcherAndResolver(t, Config{})
	resolver.set("rebind.example", net.ParseIP("127.0.0.1"))

	url := fmt.Sprintf("http://rebind.example:%s/video.mp4", serverPort(t, srv))
	if _, err := fetcher.Fetch(context.Background(), url, FetchOptions{MaxBytes: 1024}); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if got := resolver.callCount("rebind.example"); got != 1 {
		t.Errorf("resolver was consulted %d times for this hop, want exactly 1", got)
	}
}

// TestPinnedDialContext_ConnectsToGivenIPRegardlessOfHost is a direct,
// network-only test of the dial-pinning primitive itself: it hands the
// dialer a completely made-up host:port, and asserts it still connects
// to a real listener bound to the pinned IP — proving the pin, not
// whatever address string happens to be passed in, is what decides
// where the connection actually goes.
func TestPinnedDialContext_ConnectsToGivenIPRegardlessOfHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			close(accepted)
			conn.Close()
		}
	}()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}

	dial := pinnedDialContext(net.ParseIP("127.0.0.1"), time.Second)
	conn, err := dial(context.Background(), "tcp", "this-hostname-does-not-exist.invalid:"+port)
	if err != nil {
		t.Fatalf("dial() error = %v", err)
	}
	defer conn.Close()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("listener never accepted a connection — dial did not reach the pinned IP")
	}
}

func TestFetchError_IsPermanent(t *testing.T) {
	tests := []struct {
		kind       FetchErrorKind
		statusCode int
		want       bool
	}{
		{FetchErrorBlocked, 0, true},
		{FetchErrorTooManyRedirects, 0, true},
		{FetchErrorResponseTooLarge, 0, true},
		{FetchErrorUnacceptableContentType, 0, true},
		{FetchErrorUnacceptableStatus, 404, true},
		{FetchErrorUnacceptableStatus, 503, false},
		{FetchErrorTimeout, 0, false},
		{FetchErrorNetwork, 0, false},
	}
	for _, tt := range tests {
		err := &FetchError{Kind: tt.kind, StatusCode: tt.statusCode}
		if got := err.IsPermanent(); got != tt.want {
			t.Errorf("FetchError{Kind: %v, StatusCode: %d}.IsPermanent() = %v, want %v", tt.kind, tt.statusCode, got, tt.want)
		}
	}
}

// asFetchError is errors.As without importing "errors" into every
// test — small enough, and used everywhere in this file, to earn a
// one-line helper.
func asFetchError(err error, target **FetchError) bool {
	fe, ok := err.(*FetchError)
	if !ok {
		return false
	}
	*target = fe
	return true
}
