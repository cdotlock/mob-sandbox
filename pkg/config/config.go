package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var Version = "dev"

type ServerConfig struct {
	Mode       string    `yaml:"mode"`
	PublicIP   string    `yaml:"public_ip"`
	Domain     string    `yaml:"domain"`
	APIKey     string    `yaml:"daytona_api_key"`
	InstallDir string    `yaml:"install_dir"`
	Ports      Ports     `yaml:"ports"`
	LLM        LLMConfig `yaml:"llm"`
	DNS        DNSConfig `yaml:"dns"`
}

type Ports struct {
	API       int `yaml:"api"`
	SSH       int `yaml:"ssh"`
	OpenHands int `yaml:"openhands"`
	Proxy     int `yaml:"proxy"`
	Control   int `yaml:"control"`
}

type LLMConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type DNSConfig struct {
	Provider string `yaml:"provider"`
	Token    string `yaml:"token"`
	Secret   string `yaml:"secret"`
}

type ClientConfig struct {
	Server   string `yaml:"server"`
	APIKey   string `yaml:"api_key"`
	SSHHost  string `yaml:"ssh_host"`
	SSHPort  int    `yaml:"ssh_port"`
	OpenHands string `yaml:"openhands"`
	Control  string `yaml:"control"`
	Mode     string `yaml:"mode"`
}

func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Mode:       "ip",
		InstallDir: "/opt/mob-sandbox",
		Ports: Ports{
			API:       3986,
			SSH:       2222,
			OpenHands: 3000,
			Proxy:     4000,
			Control:   9876,
		},
		LLM: LLMConfig{
			BaseURL: "https://api.deepseek.com",
			Model:   "deepseek-v4-pro",
		},
	}
}

func ServerConfigPath() string {
	return "/etc/mob-server/config.yaml"
}

func ClientConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mob", "config.yaml")
}

func LoadServerConfig() (*ServerConfig, error) {
	return loadYAML[ServerConfig](ServerConfigPath())
}

func LoadClientConfig() (*ClientConfig, error) {
	return loadYAML[ClientConfig](ClientConfigPath())
}

func (c *ServerConfig) Save() error {
	return saveYAML(ServerConfigPath(), c)
}

func (c *ClientConfig) Save() error {
	return saveYAML(ClientConfigPath(), c)
}

func loadYAML[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg T
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveYAML(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
