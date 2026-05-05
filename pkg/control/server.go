package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Name      string     `json:"name" yaml:"name"`
	SandboxID string     `json:"sandbox_id" yaml:"sandbox_id"`
	Port      int        `json:"port" yaml:"port"`
	Subdomain string     `json:"subdomain,omitempty" yaml:"subdomain,omitempty"`
	URL       string     `json:"url" yaml:"url"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

type ExposeRequest struct {
	SandboxID  string `json:"sandbox_id"`
	Port       int    `json:"port"`
	Name       string `json:"name"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	Permanent  bool   `json:"permanent,omitempty"`
}

type routeStore struct {
	Routes []Route `yaml:"routes"`
}

var (
	exposeNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	sandboxIDPattern  = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

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
	}
	if s.daytonaURL != "" && s.apiKey != "" {
		s.daytona = daytona.NewClient(s.daytonaURL, s.apiKey)
	}
	return s
}

func (s *Server) Start() error {
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

	target := &url.URL{Scheme: "http", Host: "sandbox.internal"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return s.dialSandbox(ctx, route)
		},
		DisableKeepAlives: true,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("expose proxy %s -> %s:%d failed: %v", route.Name, route.SandboxID, route.Port, err)
		http.Error(w, "sandbox upstream unavailable", http.StatusBadGateway)
	}

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Scheme = "http"
		req.URL.Host = target.Host
		req.Host = r.Host
		req.Header.Set("X-Forwarded-Host", r.Host)
		req.Header.Set("X-Forwarded-Proto", forwardedProto(r))
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) addRoute(req ExposeRequest) (Route, error) {
	route := Route{
		Name:      req.Name,
		SandboxID: req.SandboxID,
		Port:      req.Port,
		Subdomain: s.subdomain(req.Name),
		URL:       s.publicURL(req.Name),
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

func (s *Server) dialSandbox(ctx context.Context, route Route) (net.Conn, error) {
	if s.daytona == nil {
		return nil, errors.New("daytona client not configured")
	}

	access, err := s.daytona.GetSSHAccess(route.SandboxID)
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
	client := ssh.NewClient(conn, chans, reqs)
	upstream, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", route.Port))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sandbox dial: %w", err)
	}
	return &sshBackedConn{Conn: upstream, client: client}, nil
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
	return nil
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
