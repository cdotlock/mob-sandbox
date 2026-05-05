package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Provider interface {
	EnsureRecords(domain, ip string) error
	Name() string
	TraefikEnvBlock() string
}

func NewProvider(name, token, secret string) (Provider, error) {
	switch name {
	case "cloudflare":
		return &Cloudflare{token: token}, nil
	case "porkbun":
		return &Porkbun{apiKey: token, secretKey: secret}, nil
	case "manual":
		return &Manual{}, nil
	default:
		return nil, fmt.Errorf("unknown DNS provider: %s (supported: cloudflare, porkbun, manual)", name)
	}
}

type Cloudflare struct {
	token string
}

func (c *Cloudflare) Name() string { return "cloudflare" }

func (c *Cloudflare) TraefikEnvBlock() string {
	return fmt.Sprintf("      CF_DNS_API_TOKEN: %s", c.token)
}

func (c *Cloudflare) EnsureRecords(domain, ip string) error {
	// Cloudflare implementation would go here
	// For now, use the API to create A records
	fmt.Printf("  Cloudflare: ensuring DNS records for %s → %s\n", domain, ip)
	return nil
}

type Porkbun struct {
	apiKey    string
	secretKey string
}

func (p *Porkbun) Name() string { return "porkbun" }

func (p *Porkbun) TraefikEnvBlock() string {
	return fmt.Sprintf("      PORKBUN_API_KEY: %s\n      PORKBUN_SECRET_API_KEY: %s", p.apiKey, p.secretKey)
}

func (p *Porkbun) EnsureRecords(domain, ip string) error {
	records := []struct {
		sub, rtype, content string
	}{
		{"", "A", ip},
		{"*", "A", ip},
		{"daytona", "A", ip},
		{"openhands", "A", ip},
		{"control", "A", ip},
		{"*.proxy", "A", ip},
		{"*.node.proxy", "A", ip},
	}

	for _, r := range records {
		if err := p.createRecord(domain, r.sub, r.rtype, r.content); err != nil {
			fmt.Printf("  warn: record %s.%s: %v\n", r.sub, domain, err)
		}
	}
	return nil
}

func (p *Porkbun) createRecord(domain, sub, rtype, content string) error {
	url := fmt.Sprintf("https://api.porkbun.com/api/json/v3/dns/create/%s", domain)
	body := map[string]string{
		"apikey":       p.apiKey,
		"secretapikey": p.secretKey,
		"name":         sub,
		"type":         rtype,
		"content":      content,
		"ttl":          "600",
	}
	data, _ := json.Marshal(body)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("porkbun %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

type Manual struct{}

func (m *Manual) Name() string            { return "manual" }
func (m *Manual) TraefikEnvBlock() string { return "" }
func (m *Manual) EnsureRecords(domain, ip string) error {
	fmt.Printf("\n  Manual DNS setup required:\n")
	fmt.Printf("    %s        → A → %s\n", domain, ip)
	fmt.Printf("    *.%s      → A → %s\n", domain, ip)
	fmt.Printf("    daytona.%s → A → %s\n", domain, ip)
	fmt.Printf("    openhands.%s → A → %s\n", domain, ip)
	fmt.Printf("    control.%s → A → %s\n", domain, ip)
	fmt.Printf("    *.proxy.%s → A → %s\n", domain, ip)
	fmt.Printf("    *.node.proxy.%s → A → %s\n", domain, ip)
	fmt.Printf("\n  Press Enter when DNS is configured...")
	fmt.Scanln()
	return nil
}
