package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cdotlock/mob-sandbox/pkg/daytona"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

type Server struct {
	port       int
	mode       string
	domain     string
	publicIP   string
	apiKey     string
	daytonaURL string
	sshHost    string
	sshPort    int
	routesPath string
	storePath  string
	daytona    *daytona.Client
	mu         sync.Mutex
	tunnelMu   sync.Mutex
	tunnels    map[string]*routeTunnel
}

type Options struct {
	Port       int
	Mode       string
	Domain     string
	PublicIP   string
	APIKey     string
	DaytonaURL string
	SSHHost    string
	SSHPort    int
	RoutesPath string
	StorePath  string
}

type Route struct {
	Name         string     `json:"name" yaml:"name"`
	SandboxID    string     `json:"sandbox_id" yaml:"sandbox_id"`
	Port         int        `json:"port" yaml:"port"`
	Subdomain    string     `json:"subdomain,omitempty" yaml:"subdomain,omitempty"`
	URL          string     `json:"url" yaml:"url"`
	StartCommand string     `json:"start_command,omitempty" yaml:"start_command,omitempty"`
	HealthPath   string     `json:"health_path,omitempty" yaml:"health_path,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

type ExposeRequest struct {
	SandboxID    string `json:"sandbox_id"`
	Port         int    `json:"port"`
	Name         string `json:"name"`
	TTLSeconds   int    `json:"ttl_seconds,omitempty"`
	Permanent    bool   `json:"permanent,omitempty"`
	StartCommand string `json:"start_command,omitempty"`
	HealthPath   string `json:"health_path,omitempty"`
}

type routeStore struct {
	Routes []Route `yaml:"routes"`
}

type routeTunnel struct {
	route     Route
	listener  net.Listener
	clients   []*ssh.Client
	proxy     *httputil.ReverseProxy
	transport *http.Transport
	localAddr string
	next      uint64
	done      chan struct{}
	closeOnce sync.Once
}

var (
	exposeNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	sandboxIDPattern  = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

const routeTunnelClientCount = 6

func NewServer(port int, domain, apiKey string) *Server {
	return NewServerWithOptions(Options{
		Port:   port,
		Mode:   "domain",
		Domain: domain,
		APIKey: apiKey,
	})
}

func NewServerWithOptions(opts Options) *Server {
	if opts.Port == 0 {
		opts.Port = 9876
	}
	if opts.Mode == "" {
		opts.Mode = "ip"
	}
	if opts.SSHHost == "" {
		opts.SSHHost = "127.0.0.1"
	}
	if opts.SSHPort == 0 {
		opts.SSHPort = 2222
	}
	if opts.RoutesPath == "" {
		opts.RoutesPath = "/etc/traefik/dynamic/routes.yml"
	}
	if opts.StorePath == "" {
		opts.StorePath = "/etc/mob-server/exposures.yml"
	}

	s := &Server{
		port:       opts.Port,
		mode:       opts.Mode,
		domain:     opts.Domain,
		publicIP:   opts.PublicIP,
		apiKey:     opts.APIKey,
		daytonaURL: opts.DaytonaURL,
		sshHost:    opts.SSHHost,
		sshPort:    opts.SSHPort,
		routesPath: opts.RoutesPath,
		storePath:  opts.StorePath,
		tunnels:    make(map[string]*routeTunnel),
	}
	if s.daytonaURL != "" && s.apiKey != "" {
		s.daytona = daytona.NewClient(s.daytonaURL, s.apiKey)
	}
	return s
}

func (s *Server) Start() error {
	go s.guardExposures(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /control/v1/expose", s.withAuth(s.handleCreateExpose))
	mux.HandleFunc("GET /control/v1/expose", s.withAuth(s.handleListExpose))
	mux.HandleFunc("DELETE /control/v1/expose/", s.withAuth(s.handleDeleteExpose))
	mux.HandleFunc("GET /control/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", s.handlePublicProxy)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("control API and expose proxy listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleCreateExpose(w http.ResponseWriter, r *http.Request) {
	var req ExposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, 400)
		return
	}
	if req.Name == "" || req.SandboxID == "" || req.Port == 0 {
		http.Error(w, `{"error":"name, sandbox_id, port required"}`, 400)
		return
	}
	if err := validateExposeRequest(req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 400)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	route, err := s.addRoute(req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(route)
}

func (s *Server) handleListExpose(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes, err := s.listCustomRoutes()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
		return
	}
	json.NewEncoder(w).Encode(routes)
}

func (s *Server) handleDeleteExpose(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/control/v1/expose/")
	if name == "" {
		http.Error(w, `{"error":"name required"}`, 400)
		return
	}
	if !exposeNamePattern.MatchString(name) {
		http.Error(w, `{"error":"invalid name"}`, 400)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.removeRoute(name); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
		return
	}
	w.Write([]byte(`{"status":"deleted"}`))
}

func (s *Server) handlePublicProxy(w http.ResponseWriter, r *http.Request) {
	name := s.routeNameFromHost(r.Host)
	if name == "" {
		http.NotFound(w, r)
		return
	}

	route, ok, err := s.findRoute(name)
	if err != nil {
		http.Error(w, fmt.Sprintf("load route: %v", err), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if route.ExpiresAt != nil && time.Now().After(*route.ExpiresAt) {
		http.Error(w, "exposure expired", http.StatusGone)
		return
	}

	tunnel, err := s.ensureRouteTunnel(r.Context(), route)
	if err != nil {
		log.Printf("expose proxy %s -> %s:%d tunnel unavailable: %v", route.Name, route.SandboxID, route.Port, err)
		http.Error(w, "sandbox upstream unavailable", http.StatusBadGateway)
		return
	}

	tunnel.proxy.ServeHTTP(w, r)
}

func (s *Server) addRoute(req ExposeRequest) (Route, error) {
	route := Route{
		Name:         req.Name,
		SandboxID:    req.SandboxID,
		Port:         req.Port,
		Subdomain:    s.subdomain(req.Name),
		URL:          s.publicURL(req.Name),
		StartCommand: strings.TrimSpace(req.StartCommand),
		HealthPath:   strings.TrimSpace(req.HealthPath),
	}
	if !req.Permanent && req.TTLSeconds > 0 {
		expiresAt := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
		route.ExpiresAt = &expiresAt
	}

	store, err := s.loadStore()
	if err != nil {
		return Route{}, err
	}
	replaced := false
	for i := range store.Routes {
		if store.Routes[i].Name == route.Name {
			store.Routes[i] = route
			replaced = true
			break
		}
	}
	if !replaced {
		store.Routes = append(store.Routes, route)
	}

	if err := s.saveStore(store); err != nil {
		return Route{}, err
	}
	if err := s.ensureTraefikRoute(route); err != nil {
		return Route{}, err
	}
	return route, nil
}

func (s *Server) removeRoute(name string) error {
	store, err := s.loadStore()
	if err != nil {
		return err
	}
	filtered := store.Routes[:0]
	for _, route := range store.Routes {
		if route.Name != name {
			filtered = append(filtered, route)
		}
	}
	store.Routes = filtered
	if err := s.saveStore(store); err != nil {
		return err
	}
	s.closeRouteTunnel(name, nil)
	return s.removeTraefikRoute(name)
}

func (s *Server) listCustomRoutes() ([]Route, error) {
	store, err := s.loadStore()
	if err != nil {
		return nil, err
	}
	for i := range store.Routes {
		store.Routes[i].Subdomain = s.subdomain(store.Routes[i].Name)
		store.Routes[i].URL = s.publicURL(store.Routes[i].Name)
	}
	return store.Routes, nil
}

func (s *Server) findRoute(name string) (Route, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadStore()
	if err != nil {
		return Route{}, false, err
	}
	for _, route := range store.Routes {
		if route.Name == name {
			return route, true, nil
		}
	}
	return Route{}, false, nil
}

func (s *Server) ensureTraefikRoute(route Route) error {
	if s.mode != "domain" || s.domain == "" {
		return nil
	}

	routes, err := s.loadRoutes()
	if err != nil {
		return err
	}
	httpSection, _ := routes["http"].(map[string]any)
	if httpSection == nil {
		httpSection = map[string]any{}
		routes["http"] = httpSection
	}
	routers, _ := httpSection["routers"].(map[string]any)
	if routers == nil {
		routers = map[string]any{}
		httpSection["routers"] = routers
	}
	services, _ := httpSection["services"].(map[string]any)
	if services == nil {
		services = map[string]any{}
		httpSection["services"] = services
	}

	routerName := "custom-" + route.Name
	serviceName := "custom-svc-" + route.Name
	routers[routerName] = map[string]any{
		"rule":        fmt.Sprintf("Host(`%s.%s`)", route.Name, s.domain),
		"entryPoints": []string{"websecure"},
		"service":     serviceName,
		"tls":         map[string]any{"certResolver": "le"},
	}
	services[serviceName] = map[string]any{
		"loadBalancer": map[string]any{
			"servers": []map[string]string{{
				"url": fmt.Sprintf("http://host.docker.internal:%d", s.port),
			}},
		},
	}
	return s.saveRoutes(routes)
}

func (s *Server) removeTraefikRoute(name string) error {
	if s.mode != "domain" || s.domain == "" {
		return nil
	}

	routes, err := s.loadRoutes()
	if err != nil {
		return err
	}
	httpSection, _ := routes["http"].(map[string]any)
	if httpSection == nil {
		return nil
	}
	routers, _ := httpSection["routers"].(map[string]any)
	services, _ := httpSection["services"].(map[string]any)

	delete(routers, "custom-"+name)
	delete(services, "custom-svc-"+name)
	return s.saveRoutes(routes)
}

func (s *Server) ensureRouteTunnel(ctx context.Context, route Route) (*routeTunnel, error) {
	if s.daytona == nil {
		return nil, errors.New("daytona client not configured")
	}

	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()

	if existing := s.tunnels[route.Name]; existing != nil && existing.matches(route) {
		return existing, nil
	}
	if existing := s.tunnels[route.Name]; existing != nil {
		existing.Close()
		delete(s.tunnels, route.Name)
	}

	clients, err := s.sshClientsForSandbox(ctx, route.SandboxID, routeTunnelClientCount)
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		closeSSHClients(clients)
		return nil, fmt.Errorf("local tunnel listen: %w", err)
	}

	tunnel := &routeTunnel{
		route:     route,
		listener:  listener,
		clients:   clients,
		localAddr: listener.Addr().String(),
		done:      make(chan struct{}),
	}
	tunnel.proxy, tunnel.transport = s.newTunnelProxy(route, tunnel)
	s.tunnels[route.Name] = tunnel

	go s.serveRouteTunnel(tunnel)
	go s.keepRouteTunnelAlive(tunnel)
	log.Printf("expose tunnel %s -> %s:%d listening on %s", route.Name, route.SandboxID, route.Port, tunnel.localAddr)
	return tunnel, nil
}

func (s *Server) newTunnelProxy(route Route, tunnel *routeTunnel) (*httputil.ReverseProxy, *http.Transport) {
	target := &url.URL{Scheme: "http", Host: tunnel.localAddr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   256,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("expose proxy %s -> %s:%d via %s failed: %v", route.Name, route.SandboxID, route.Port, tunnel.localAddr, err)
		s.closeRouteTunnel(route.Name, tunnel)
		http.Error(w, "sandbox upstream unavailable", http.StatusBadGateway)
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalHost := req.Host
		proto := forwardedProto(req)
		originalDirector(req)
		req.Host = originalHost
		req.Header.Set("X-Forwarded-Host", originalHost)
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	return proxy, transport
}

func (s *Server) serveRouteTunnel(tunnel *routeTunnel) {
	for {
		localConn, err := tunnel.listener.Accept()
		if err != nil {
			return
		}
		go s.handleRouteTunnelConn(tunnel, localConn)
	}
}

func (s *Server) handleRouteTunnelConn(tunnel *routeTunnel, localConn net.Conn) {
	clients := tunnel.clients
	if len(clients) == 0 {
		localConn.Close()
		s.closeRouteTunnel(tunnel.route.Name, tunnel)
		return
	}

	idx := atomic.AddUint64(&tunnel.next, 1)
	client := clients[int(idx%uint64(len(clients)))]
	remoteConn, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.route.Port))
	if err != nil {
		localConn.Close()
		log.Printf("expose tunnel %s -> %s:%d dial failed: %v", tunnel.route.Name, tunnel.route.SandboxID, tunnel.route.Port, err)
		s.closeRouteTunnel(tunnel.route.Name, tunnel)
		return
	}
	copyBidirectional(localConn, remoteConn)
}

func (s *Server) keepRouteTunnelAlive(tunnel *routeTunnel) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-tunnel.done:
			return
		case <-ticker.C:
			for _, client := range tunnel.clients {
				_, _, err := client.SendRequest("keepalive@mob-sandbox", true, nil)
				if err != nil {
					log.Printf("expose tunnel %s -> %s:%d keepalive failed: %v", tunnel.route.Name, tunnel.route.SandboxID, tunnel.route.Port, err)
					s.closeRouteTunnel(tunnel.route.Name, tunnel)
					return
				}
			}
		}
	}
}

func copyBidirectional(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		a.Close()
		b.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		a.Close()
		b.Close()
		done <- struct{}{}
	}()
	<-done
}

func (s *Server) closeRouteTunnel(name string, expected *routeTunnel) {
	s.tunnelMu.Lock()
	tunnel := s.tunnels[name]
	if tunnel == nil || (expected != nil && tunnel != expected) {
		s.tunnelMu.Unlock()
		return
	}
	delete(s.tunnels, name)
	s.tunnelMu.Unlock()

	tunnel.Close()
}

func (t *routeTunnel) matches(route Route) bool {
	return t.route.SandboxID == route.SandboxID && t.route.Port == route.Port
}

func (t *routeTunnel) Close() {
	t.closeOnce.Do(func() {
		if t.done != nil {
			close(t.done)
		}
		if t.transport != nil {
			t.transport.CloseIdleConnections()
		}
		if t.listener != nil {
			t.listener.Close()
		}
		closeSSHClients(t.clients)
	})
}

func closeSSHClients(clients []*ssh.Client) {
	for _, client := range clients {
		if client != nil {
			client.Close()
		}
	}
}

func (s *Server) dialSandbox(ctx context.Context, route Route) (net.Conn, error) {
	client, err := s.sshClientForSandbox(ctx, route.SandboxID)
	if err != nil {
		return nil, err
	}
	upstream, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", route.Port))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sandbox dial: %w", err)
	}
	return &sshBackedConn{Conn: upstream, client: client}, nil
}

func (s *Server) sshClientForSandbox(ctx context.Context, sandboxID string) (*ssh.Client, error) {
	if s.daytona == nil {
		return nil, errors.New("daytona client not configured")
	}

	access, err := s.daytona.GetSSHAccess(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("ssh access: %w", err)
	}

	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", s.sshHost, s.sshPort))
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            access.Token,
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config: ssh.Config{
			KeyExchanges: []string{"curve25519-sha256"},
		},
		Timeout: 10 * time.Second,
	}

	conn, chans, reqs, err := ssh.NewClientConn(raw, fmt.Sprintf("%s:%d", s.sshHost, s.sshPort), config)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("ssh client: %w", err)
	}
	return ssh.NewClient(conn, chans, reqs), nil
}

func (s *Server) sshClientsForSandbox(ctx context.Context, sandboxID string, count int) ([]*ssh.Client, error) {
	if count < 1 {
		count = 1
	}

	clients := make([]*ssh.Client, 0, count)
	for i := 0; i < count; i++ {
		client, err := s.sshClientForSandbox(ctx, sandboxID)
		if err != nil {
			if len(clients) > 0 {
				log.Printf("expose tunnel %s using %d/%d ssh clients: %v", sandboxID, len(clients), count, err)
				return clients, nil
			}
			closeSSHClients(clients)
			return nil, err
		}
		clients = append(clients, client)
	}
	return clients, nil
}

type sshBackedConn struct {
	net.Conn
	client *ssh.Client
}

func (c *sshBackedConn) Close() error {
	err := c.Conn.Close()
	clientErr := c.client.Close()
	if err != nil {
		return err
	}
	return clientErr
}

func (s *Server) routeNameFromHost(hostport string) string {
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = strings.ToLower(h)
	}
	if s.domain != "" {
		suffix := "." + strings.ToLower(s.domain)
		if strings.HasSuffix(host, suffix) {
			return strings.TrimSuffix(host, suffix)
		}
	}
	if s.publicIP != "" {
		suffix := "." + s.publicIP + ".sslip.io"
		if strings.HasSuffix(host, suffix) {
			return strings.TrimSuffix(host, suffix)
		}
	}
	return ""
}

func (s *Server) subdomain(name string) string {
	if s.mode == "domain" && s.domain != "" {
		return fmt.Sprintf("%s.%s", name, s.domain)
	}
	if s.publicIP == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.sslip.io", name, s.publicIP)
}

func (s *Server) publicURL(name string) string {
	if s.mode == "domain" && s.domain != "" {
		return fmt.Sprintf("https://%s.%s", name, s.domain)
	}
	if s.publicIP == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", s.port)
	}
	return fmt.Sprintf("http://%s.%s.sslip.io:%d", name, s.publicIP, s.port)
}

func (s *Server) loadStore() (*routeStore, error) {
	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &routeStore{}, nil
		}
		return nil, err
	}
	var store routeStore
	if err := yaml.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return &store, nil
}

func (s *Server) saveStore(store *routeStore) error {
	if err := os.MkdirAll(filepath.Dir(s.storePath), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(store)
	if err != nil {
		return err
	}
	return os.WriteFile(s.storePath, data, 0600)
}

func (s *Server) loadRoutes() (map[string]any, error) {
	data, err := os.ReadFile(s.routesPath)
	if err != nil {
		return nil, err
	}
	var routes map[string]any
	if err := yaml.Unmarshal(data, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func (s *Server) saveRoutes(routes map[string]any) error {
	data, err := yaml.Marshal(routes)
	if err != nil {
		return err
	}
	return os.WriteFile(s.routesPath, data, 0644)
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			http.Error(w, `{"error":"control api key not configured"}`, http.StatusServiceUnavailable)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != s.apiKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func validateExposeRequest(req ExposeRequest) error {
	if req.Port < 1 || req.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.TTLSeconds < 0 {
		return fmt.Errorf("ttl_seconds must be non-negative")
	}
	if !exposeNamePattern.MatchString(req.Name) {
		return fmt.Errorf("name must be a DNS label: lowercase letters, numbers, and hyphens only")
	}
	switch req.Name {
	case "daytona", "openhands", "control", "node", "proxy", "www":
		return fmt.Errorf("name %q is reserved", req.Name)
	}
	if !sandboxIDPattern.MatchString(req.SandboxID) {
		return fmt.Errorf("invalid sandbox_id")
	}
	if strings.Contains(req.StartCommand, "\x00") {
		return fmt.Errorf("start_command contains a NUL byte")
	}
	if len(req.StartCommand) > 4096 {
		return fmt.Errorf("start_command is too long")
	}
	if req.HealthPath != "" && !strings.HasPrefix(req.HealthPath, "/") {
		return fmt.Errorf("health_path must start with /")
	}
	if strings.ContainsAny(req.HealthPath, "\r\n\x00") {
		return fmt.Errorf("health_path contains invalid characters")
	}
	return nil
}

func (s *Server) guardExposures(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		if err := s.reconcileExposures(ctx); err != nil {
			log.Printf("expose guardian: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reconcileExposures(ctx context.Context) error {
	if s.daytona == nil {
		return nil
	}

	routes, err := s.activeRoutes()
	if err != nil {
		return err
	}
	for _, route := range routes {
		if err := s.ensureRouteReady(ctx, route); err != nil {
			log.Printf("expose guardian: %s -> %s:%d not ready: %v", route.Name, route.SandboxID, route.Port, err)
		}
	}
	return nil
}

func (s *Server) activeRoutes() ([]Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	store, err := s.loadStore()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	routes := make([]Route, 0, len(store.Routes))
	for _, route := range store.Routes {
		if route.ExpiresAt != nil && now.After(*route.ExpiresAt) {
			continue
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func (s *Server) ensureRouteReady(ctx context.Context, route Route) error {
	if s.daytona == nil {
		return nil
	}
	if err := s.ensureSandboxStarted(ctx, route.SandboxID); err != nil {
		return err
	}

	tunnel, err := s.ensureRouteTunnel(ctx, route)
	if err != nil {
		return err
	}
	if s.routeHealthy(ctx, route, tunnel) {
		return nil
	}

	// Recreate once before running the start command. This distinguishes a
	// stale SSH tunnel from an application that is genuinely down.
	s.closeRouteTunnel(route.Name, tunnel)
	tunnel, err = s.ensureRouteTunnel(ctx, route)
	if err != nil {
		return err
	}
	if s.routeHealthy(ctx, route, tunnel) {
		return nil
	}

	if route.StartCommand == "" {
		return errors.New("sandbox port is not reachable and no start_command is configured")
	}
	if err := s.runSandboxCommand(ctx, route.SandboxID, route.StartCommand); err != nil {
		if waitErr := s.waitRouteHealthy(ctx, route, 10*time.Second); waitErr == nil {
			return nil
		}
		return fmt.Errorf("start command: %w", err)
	}
	return s.waitRouteHealthy(ctx, route, 30*time.Second)
}

func (s *Server) ensureSandboxStarted(ctx context.Context, sandboxID string) error {
	sb, err := s.daytona.GetSandbox(sandboxID)
	if err != nil {
		return fmt.Errorf("sandbox status: %w", err)
	}
	if isSandboxStarted(sb.State) {
		return nil
	}
	log.Printf("expose guardian: starting sandbox %s (%s)", sandboxID, sb.State)
	if err := s.daytona.StartSandbox(sandboxID); err != nil {
		return fmt.Errorf("start sandbox: %w", err)
	}
	return s.waitSandboxStarted(ctx, sandboxID, 2*time.Minute)
}

func (s *Server) waitSandboxStarted(ctx context.Context, sandboxID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		sb, err := s.daytona.GetSandbox(sandboxID)
		if err == nil && isSandboxStarted(sb.State) {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("sandbox did not start: %w", err)
			}
			return errors.New("sandbox did not start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func isSandboxStarted(state string) bool {
	switch strings.ToLower(state) {
	case "started", "running", "ready":
		return true
	default:
		return false
	}
}

func (s *Server) routeHealthy(ctx context.Context, route Route, tunnel *routeTunnel) bool {
	path := route.HealthPath
	if path == "" {
		path = "/"
	}

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+tunnel.localAddr+path, nil)
	if err != nil {
		return false
	}
	req.Host = route.Subdomain

	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if route.HealthPath != "" {
		return resp.StatusCode >= 200 && resp.StatusCode < 400
	}
	return resp.StatusCode < 500
}

func (s *Server) waitRouteHealthy(ctx context.Context, route Route, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		tunnel, err := s.ensureRouteTunnel(ctx, route)
		if err == nil && s.routeHealthy(ctx, route, tunnel) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("sandbox port did not become reachable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) runSandboxCommand(ctx context.Context, sandboxID, command string) error {
	output, err := s.runSandboxCommandOutput(ctx, sandboxID, command, 30*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (s *Server) runSandboxCommandOutput(ctx context.Context, sandboxID, command string, timeout time.Duration) (string, error) {
	client, err := s.sshClientForSandbox(ctx, sandboxID)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		out, err := session.CombinedOutput(command)
		done <- result{output: out, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return string(res.output), res.err
		}
		return string(res.output), nil
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case <-time.After(timeout):
		_ = session.Close()
		return "", errors.New("command timed out")
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func forwardedProto(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
