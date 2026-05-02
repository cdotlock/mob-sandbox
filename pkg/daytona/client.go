package daytona

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type Sandbox struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type SSHAccess struct {
	Token string `json:"token"`
}

type Snapshot struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type SignedURL struct {
	URL string `json:"url"`
}

func (c *Client) Health() error {
	_, err := c.get("/api/health")
	return err
}

func (c *Client) GetInfo() (map[string]any, error) {
	data, err := c.get("/api/info")
	if err != nil {
		return nil, err
	}
	var info map[string]any
	return info, json.Unmarshal(data, &info)
}

func (c *Client) CreateSandbox(snapshotID string) (*Sandbox, error) {
	body := map[string]string{"snapshot": snapshotID}
	data, err := c.post("/api/sandbox", body)
	if err != nil {
		return nil, err
	}
	var sb Sandbox
	return &sb, json.Unmarshal(data, &sb)
}

func (c *Client) ListSandboxes() ([]Sandbox, error) {
	data, err := c.get("/api/sandbox")
	if err != nil {
		return nil, err
	}
	var items []Sandbox
	return items, json.Unmarshal(data, &items)
}

func (c *Client) GetSandbox(id string) (*Sandbox, error) {
	data, err := c.get("/api/sandbox/" + id)
	if err != nil {
		return nil, err
	}
	var sb Sandbox
	return &sb, json.Unmarshal(data, &sb)
}

func (c *Client) DeleteSandbox(id string) error {
	return c.delete("/api/sandbox/" + id)
}

func (c *Client) GetSSHAccess(id string) (*SSHAccess, error) {
	data, err := c.post("/api/sandbox/"+id+"/ssh-access", nil)
	if err != nil {
		return nil, err
	}
	var access SSHAccess
	return &access, json.Unmarshal(data, &access)
}

func (c *Client) BuildPreviewURL(id string, port int, domain string) string {
	return fmt.Sprintf("https://%d-%s.node.proxy.%s", port, id, domain)
}

func (c *Client) CreateSnapshot(imageName string) (*Snapshot, error) {
	body := map[string]any{
		"name":       "mob-sandbox:1.0",
		"imageName":  imageName,
		"entrypoint": []string{"sleep", "infinity"},
		"cpu":        1,
		"memory":     2,
		"disk":       10,
	}
	data, err := c.post("/api/snapshots", body)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	return &snap, json.Unmarshal(data, &snap)
}

func (c *Client) GetSnapshot(id string) (*Snapshot, error) {
	data, err := c.get("/api/snapshots/" + id)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	return &snap, json.Unmarshal(data, &snap)
}

func (c *Client) ListSnapshots() ([]Snapshot, error) {
	data, err := c.get("/api/snapshots")
	if err != nil {
		return nil, err
	}
	var items []Snapshot
	return items, json.Unmarshal(data, &items)
}

func (c *Client) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) post(path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest("POST", c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req)
}

func (c *Client) delete(path string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	_, err = c.do(req)
	return err
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("API %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, string(data))
	}
	return data, nil
}
