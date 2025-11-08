package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultDebugURL = "http://127.0.0.1:9222"
	defaultListen   = ":9223"
	requestTimeout  = 5 * time.Second
)

type versionInfo struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type proxyServer struct {
	chromiumURL *url.URL
	listenAddr  string

	upgrader websocket.Upgrader
	dialer   websocket.Dialer
	client   *http.Client

	mu          sync.RWMutex
	debuggerURL string
}

func newProxyServer(chromiumEndpoint, listenAddr string) (*proxyServer, error) {
	if chromiumEndpoint == "" {
		chromiumEndpoint = defaultDebugURL
	}

	parsed, err := url.Parse(chromiumEndpoint)
	if err != nil {
		return nil, err
	}

	if parsed.Scheme == "" {
		return nil, errors.New("chromium debugger URL must include scheme (e.g. http://)")
	}

	if listenAddr == "" {
		listenAddr = defaultListen
	}

	server := &proxyServer{
		chromiumURL: parsed,
		listenAddr:  listenAddr,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		dialer: websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: requestTimeout,
		},
		client: &http.Client{
			Timeout: requestTimeout,
		},
	}

	return server, nil
}

func (p *proxyServer) versionEndpoint() string {
	versionURL := *p.chromiumURL
	cleanPath := strings.TrimSuffix(versionURL.Path, "/")
	versionURL.Path = cleanPath + "/json/version"
	versionURL.RawQuery = ""
	versionURL.Fragment = ""
	return versionURL.String()
}

func (p *proxyServer) fetchVersionInfo(ctx context.Context) (*versionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.versionEndpoint(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	var info versionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	if info.WebSocketDebuggerURL == "" {
		return nil, errors.New("chromium /json/version response missing webSocketDebuggerUrl")
	}

	return &info, nil
}

func (p *proxyServer) ensureDebuggerURL(ctx context.Context) error {
	if current := p.getDebuggerURL(); current != "" {
		return nil
	}

	info, err := p.fetchVersionInfo(ctx)
	if err != nil {
		return err
	}

	debuggerURL := info.WebSocketDebuggerURL

	p.mu.Lock()
	p.debuggerURL = debuggerURL
	p.mu.Unlock()

	log.Printf("Chromium debugger endpoint set to %s", debuggerURL)
	return nil
}

func (p *proxyServer) getDebuggerURL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.debuggerURL
}

func (p *proxyServer) dialBackend(ctx context.Context, subprotocol string) (*websocket.Conn, *http.Response, error) {
	if err := p.ensureDebuggerURL(ctx); err != nil {
		return nil, nil, err
	}
	target := p.getDebuggerURL()

	header := http.Header{}
	if subprotocol != "" {
		header.Set("Sec-WebSocket-Protocol", subprotocol)
	}

	conn, resp, err := p.dialer.DialContext(ctx, target, header)
	return conn, resp, err
}

func (p *proxyServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	info, err := p.fetchVersionInfo(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	p.mu.Lock()
	previous := p.debuggerURL
	p.debuggerURL = info.WebSocketDebuggerURL
	p.mu.Unlock()

	if previous != info.WebSocketDebuggerURL {
		log.Printf("Chromium debugger endpoint updated to %s", info.WebSocketDebuggerURL)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]string{
		"status":               "ok",
		"browser":              info.Browser,
		"webSocketDebuggerUrl": info.WebSocketDebuggerURL,
		"protocolVersion":      info.ProtocolVersion,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Failed to encode health response: %v", err)
	}
}

func (p *proxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		p.serveWebSocket(w, r)
		return
	}

	http.NotFound(w, r)
}

func (p *proxyServer) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade incoming connection: %v", err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	backendConn, _, err := p.dialBackend(ctx, conn.Subprotocol())
	if err != nil {
		log.Printf("Failed to connect to Chromium debugger: %v", err)
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "upstream unavailable"), time.Now().Add(time.Second))
		return
	}
	defer backendConn.Close()

	errCh := make(chan error, 2)

	go mirrorWebsocket(errCh, backendConn, conn)
	go mirrorWebsocket(errCh, conn, backendConn)

	err = <-errCh
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) && err != nil {
		log.Printf("Proxy connection closed with error: %v", err)
	}
}

func mirrorWebsocket(errCh chan<- error, dst, src *websocket.Conn) {
	for {
		msgType, data, err := src.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}

		if err := dst.WriteMessage(msgType, data); err != nil {
			errCh <- err
			return
		}
	}
}

func (p *proxyServer) start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealth)
	mux.HandleFunc("/", p.handleProxy)

	server := &http.Server{
		Addr:    p.listenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	log.Printf("Chromium proxy listening on %s", p.listenAddr)
	if err := p.ensureDebuggerURL(ctx); err != nil {
		log.Printf("Initial debugger URL fetch failed: %v", err)
	}

	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	var (
		chromiumAddr string
		listenAddr   string
	)

	flag.StringVar(&chromiumAddr, "chromium", getEnv("CHROMIUM_REMOTE_DEBUGGING_URL", defaultDebugURL), "Chromium remote debugging HTTP endpoint (e.g. http://127.0.0.1:9222)")
	flag.StringVar(&listenAddr, "listen", getEnv("LISTEN_ADDR", defaultListen), "Address to listen for incoming WebSocket connections")
	flag.Parse()

	server, err := newProxyServer(chromiumAddr, listenAddr)
	if err != nil {
		log.Fatalf("Failed to create proxy server: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.start(ctx); err != nil {
		log.Fatalf("Server exited with error: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
