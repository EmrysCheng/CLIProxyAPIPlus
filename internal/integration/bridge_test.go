package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func testBridge() (*Bridge, *gin.Engine) {
	gin.SetMode(gin.TestMode)
	b := &Bridge{}
	engine := gin.New()
	engine.Use(b.Middleware())
	b.SetCore(engine)
	engine.GET("/v0/management/config", func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer core-secret" {
			c.JSON(401, gin.H{"error": "core auth"})
			return
		}
		c.Header("X-Core", "original")
		c.String(200, "core config")
	})
	engine.GET("/v1/models", func(c *gin.Context) { c.String(200, "models") })
	return b, engine
}

func TestExternalManagementFailsClosed(t *testing.T) {
	_, engine := testBridge()
	for _, path := range []string{"/", "/management.html", "/v0/management/config", "/v0/management/usage-queue"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer core-secret")
		r.Header.Set("X-Internal-Request", "true")
		engine.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatalf("%s got %d, want 503", path, w.Code)
		}
	}
}

func TestInternalTransportSkipsManagerButNotCoreAuth(t *testing.T) {
	b, engine := testBridge()
	calls := 0
	b.SetManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "admin only", 401) }))
	client := &http.Client{Transport: &Transport{Bridge: b}}
	for _, key := range []string{"wrong", "core-secret"} {
		req, _ := http.NewRequest("GET", "http://"+InternalHost+"/v0/management/config", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		want := 401
		if key == "core-secret" {
			want = 200
			if string(body) != "core config" {
				t.Fatal(string(body))
			}
		}
		if res.StatusCode != want {
			t.Fatalf("key %s got %d", key, res.StatusCode)
		}
	}
	if calls != 0 {
		t.Fatal("internal management recursively re-entered Manager")
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/v0/management/config", nil))
	if w.Code != 401 || calls != 1 {
		t.Fatal("external request bypassed Manager")
	}
}

func TestV8BoundaryAndCallback(t *testing.T) {
	b, engine := testBridge()
	b.SetManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	engine.GET("/v8/management/oauth/callback", func(c *gin.Context) { c.String(400, "state required") })
	for path, want := range map[string]int{
		"/v8/management/config/api-keys":   401,
		"/v8/management/config/api-keys-x": 404,
		"/v8/management/config/upstreams":  404,
		"/v8/management/oauth/callback":    400,
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

func TestTransportRejectsTargets(t *testing.T) {
	b, _ := testBridge()
	transport := &Transport{Bridge: b}
	for _, target := range []string{
		"https://cliproxy.internal/v0/management/config",
		"http://cliproxy.internal.evil/v0/management/config",
		"http://cliproxy.internal:8317/v0/management/config",
		"http://user@cliproxy.internal/v0/management/config",
		"http://127.0.0.1/v0/management/config",
		"http://cliproxy.internal/v0/management/../config",
		"http://cliproxy.internal/v0/management/%2e%2e/config",
		"http://cliproxy.internal/v1/chat/completions",
		"http://cliproxy.internal/v8/management/config/upstreams",
	} {
		req, _ := http.NewRequest("GET", target, nil)
		if _, err := transport.RoundTrip(req); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
}

func TestStreamingDoesNotBuffer(t *testing.T) {
	b := &Bridge{}
	release := make(chan struct{})
	exited := make(chan struct{})
	b.SetCore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(exited)
		w.Header().Set("X-Stream", "yes")
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second")
	}))
	req, _ := http.NewRequest("GET", "http://"+InternalHost+"/v0/management/logs", nil)
	res, err := (&Transport{Bridge: b}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	first := make([]byte, 5)
	if _, err := io.ReadFull(res.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "first" || res.Header.Get("X-Stream") != "yes" {
		t.Fatal("stream headers or first chunk lost")
	}
	close(release)
	rest, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "second" {
		t.Fatal(string(rest))
	}
	<-exited
}

func TestCloseCancelsHandler(t *testing.T) {
	b := &Bridge{}
	exited := make(chan struct{})
	b.SetCore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); <-r.Context().Done(); close(exited) }))
	req, _ := http.NewRequest("GET", "http://"+InternalHost+"/v0/management/logs", nil)
	res, err := (&Transport{Bridge: b}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not observe cancellation")
	}
}

func TestCanceledBeforeRoundTrip(t *testing.T) {
	b, _ := testBridge()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "http://"+InternalHost+"/v0/management/config", nil)
	if _, err := (&Transport{Bridge: b}).RoundTrip(r); err == nil {
		t.Fatal("accepted canceled request")
	}
}

func TestReadinessAndDataPlane(t *testing.T) {
	b, engine := testBridge()
	b.SetManager(http.NotFoundHandler())
	b.MarkCoreReady(true)
	for path, want := range map[string]int{"/plus-api/ready": 200, "/v1/models": 200, "/plus-api/missing": 404} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	b.MarkCoreReady(false)
	if b.Ready() {
		t.Fatal("ready after core stopped")
	}
}

func TestCanonicalPaths(t *testing.T) {
	for _, path := range []string{"/v0/management/%63onfig", "/v0//management/config", "/v0/management/./config", "/v0/management/../config"} {
		u, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		if canonical(u) {
			t.Errorf("accepted %s", path)
		}
	}
	if !canonical(&url.URL{Path: "/v0/management/config", RawQuery: "path=a%2Fb"}) {
		t.Fatal("query encoding is valid")
	}
}

func TestManagerOwnedRouteResetsGinNoRouteStatus(t *testing.T) {
	b, engine := testBridge()
	b.SetManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			http.Error(w, "admin only", 401)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	for path, want := range map[string]int{"/usage-service/info": 200, "/v0/management/usage": 200, "/status": 401} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d, want %d; body %s", path, w.Code, want, w.Body.String())
		}
	}
}

func TestDataPlaneWebSocketRetainsHijacker(t *testing.T) {
	_, engine := testBridge()
	upgrader := websocket.Upgrader{}
	engine.GET("/v1/ws-echo", func(c *gin.Context) {
		connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		kind, payload, err := connection.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		if err := connection.WriteMessage(kind, payload); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:]+"/v1/ws-echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := connection.WriteMessage(websocket.TextMessage, []byte("unwrapped")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.ReadMessage()
	if err != nil || string(payload) != "unwrapped" {
		t.Fatalf("WebSocket was intercepted: %s %v", payload, err)
	}
}

func TestTransportPanicBecomesErrorBeforeHeaders(t *testing.T) {
	b := &Bridge{}
	b.SetCore(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("test fixture") }))
	req, _ := http.NewRequest("GET", "http://"+InternalHost+"/v0/management/config", nil)
	if _, err := (&Transport{Bridge: b}).RoundTrip(req); err == nil {
		t.Fatal("panic was swallowed")
	}
}

func TestTransportContentLengthTrailersAndHead(t *testing.T) {
	b := &Bridge{}
	b.SetCore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Checksum")
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "hello")
		w.Header().Set("X-Checksum", "verified")
	}))
	tr := &Transport{Bridge: b}
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequest(method, "http://"+InternalHost+"/v0/management/config", nil)
		res, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if method == "GET" && (string(body) != "hello" || res.ContentLength != 5 || res.Trailer.Get("X-Checksum") != "verified") {
			t.Fatalf("GET: %q %+v", body, res)
		}
		if method == "HEAD" && (len(body) != 0 || res.ContentLength != 0) {
			t.Fatal("HEAD returned a body")
		}
	}
}
func TestDrainFencesNewRequestsAndWaitsForInflight(t *testing.T) {
	b, e := testBridge()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	b.SetManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(200) }))
	go func() {
		defer close(finished)
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v0/management/config", nil))
	}()
	<-entered
	b.StopAccepting()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if b.Drain(ctx) == nil {
		t.Fatal("drain returned while request is active")
	}
	close(release)
	<-finished
	if err := b.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProductUsesStoredAdminVerifierAndExplicitStatuses(t *testing.T) {
	b, e := testBridge()
	b.SetManager(http.NotFoundHandler())
	key := "first-admin"
	b.SetAuthorizer(func(_ context.Context, header string) (bool, error) { return header == "Bearer "+key, nil })
	b.Product = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "product") })
	for _, path := range []string{"/plus-api/releases", "/plus-api/releases/upstreams", "/plus-api/releases/upstreams/install", "/plus-api/runtime", "/usage-service/updates"} {
		for _, token := range []string{"core-secret", "first-admin"} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			e.ServeHTTP(w, r)
			want := 401
			if token == key {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%s: %d want %d", path, w.Code, want)
			}
		}
	}
	key = "rotated-admin"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/plus-api/releases", nil)
	r.Header.Set("Authorization", "Bearer first-admin")
	e.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("stale bootstrap credential still authorizes product API")
	}
}
