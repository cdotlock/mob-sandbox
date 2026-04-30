package control

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Server struct {
	port       int
	domain     string
	routesPath string
	mu         sync.Mutex
}

type Route struct {
	Name      string `json:"name"`
	SandboxID string `json:"sandbox_id"`
	Port      int    `json:"port"`
	Subdomain string `json:"subdomain"`
}

type ExposeRequest struct {
	SandboxID string `json:"sandbox_id"`
	Port      int    `json:"port"`
	Name      string `json:"name"`
}

func NewServer(port int, domain string) *Server {
	return &Server{
		port:       port,
		domain:     domain,
		routesPath: "/etc/traefik/dynamic/routes.yml",
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /control/v1/expose", s.handleCreateExpose)
	mux.HandleFunc("GET /control/v1/expose", s.handleListExpose)
	mux.HandleFunc("DELETE /control/v1/expose/", s.handleDeleteExpose)
	mux.HandleFunc("GET /control/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("control API listening on %s", addr)
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.addRoute(req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
		return
	}

	subdomain := fmt.Sprintf("%s.%s", req.Name, s.domain)
	json.NewEncoder(w).Encode(Route{
		Name:      req.Name,
		SandboxID: req.SandboxID,
		Port:      req.Port,
		Subdomain: subdomain,
	})
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

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.removeRoute(name); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), 500)
		return
	}
	w.Write([]byte(`{"status":"deleted"}`))
}

func (s *Server) addRoute(req ExposeRequest) error {
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

	routerName := "custom-" + req.Name
	serviceName := "custom-svc-" + req.Name

	routers[routerName] = map[string]any{
		"rule":        fmt.Sprintf("Host(`%s.%s`)", req.Name, s.domain),
		"entryPoints": []string{"websecure"},
		"service":     serviceName,
		"tls":         map[string]any{"certResolver": "le"},
	}

	// Route to the sandbox via Daytona's wildcard preview URL.
	// *.node.proxy.<domain> resolves back to this VM; Traefik then matches
	// the host on its daytona-proxy router and forwards to the sandbox.
	// (Matches the convention in pkg/daytona/client.go BuildPreviewURL.)
	targetURL := fmt.Sprintf("https://%d-%s.node.proxy.%s", req.Port, req.SandboxID, s.domain)
	services[serviceName] = map[string]any{
		"loadBalancer": map[string]any{
			"servers": []map[string]string{{"url": targetURL}},
		},
	}

	return s.saveRoutes(routes)
}

func (s *Server) removeRoute(name string) error {
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

func (s *Server) listCustomRoutes() ([]Route, error) {
	routes, err := s.loadRoutes()
	if err != nil {
		return nil, err
	}

	var result []Route
	httpSection, _ := routes["http"].(map[string]any)
	if httpSection == nil {
		return result, nil
	}
	routers, _ := httpSection["routers"].(map[string]any)
	for k := range routers {
		if strings.HasPrefix(k, "custom-") {
			name := strings.TrimPrefix(k, "custom-")
			result = append(result, Route{
				Name:      name,
				Subdomain: fmt.Sprintf("%s.%s", name, s.domain),
			})
		}
	}
	return result, nil
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
