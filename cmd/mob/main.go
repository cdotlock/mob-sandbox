package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/daytona"
	"github.com/cdotlock/mob-sandbox/pkg/remote"
	"github.com/cdotlock/mob-sandbox/pkg/ui"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:     "mob",
		Short:   "mob-sandbox client CLI",
		Version: config.Version,
	}

	root.AddCommand(
		initCmd(),
		createCmd(),
		sshCmd(),
		psCmd(),
		rmCmd(),
		forwardCmd(),
		urlCmd(),
		exposeCmd(),
		openhandsCmd(),
		powerCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadCfg() (*config.ClientConfig, error) {
	cfg, err := config.LoadClientConfig()
	if err != nil {
		return nil, fmt.Errorf("not configured — run 'mob init' first")
	}
	return cfg, nil
}

func newClient(cfg *config.ClientConfig) *daytona.Client {
	return daytona.NewClient(cfg.Server, cfg.APIKey)
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Connect to server, save config",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanner := bufio.NewScanner(os.Stdin)

			fmt.Print("  ? Server URL: ")
			scanner.Scan()
			server := strings.TrimSpace(scanner.Text())

			fmt.Print("  ? API Key: ")
			scanner.Scan()
			apiKey := strings.TrimSpace(scanner.Text())

			client := daytona.NewClient(server, apiKey)
			if err := client.Health(); err != nil {
				ui.Fail("Connection failed: %v", err)
				return err
			}
			ui.Ok("Connected")

			// Parse host from server URL
			host := server
			host = strings.TrimPrefix(host, "http://")
			host = strings.TrimPrefix(host, "https://")
			if idx := strings.Index(host, ":"); idx > 0 {
				host = host[:idx]
			}
			if idx := strings.Index(host, "/"); idx > 0 {
				host = host[:idx]
			}

			// Detect mode
			mode := "ip"
			info, err := client.GetInfo()
			if err == nil {
				if m, ok := info["mode"].(string); ok {
					mode = m
				}
			}

			cfg := &config.ClientConfig{
				Server:   server,
				APIKey:   apiKey,
				SSHHost:  host,
				SSHPort:  2222,
				Mode:     mode,
			}

			// Try to detect OpenHands
			if mode == "domain" {
				domain := host
				if strings.Contains(host, "daytona.") {
					domain = strings.TrimPrefix(host, "daytona.")
				}
				cfg.OpenHands = fmt.Sprintf("https://openhands.%s", domain)
				cfg.Control = fmt.Sprintf("https://control.%s", domain)
			} else {
				cfg.OpenHands = fmt.Sprintf("http://%s:3000", host)
				cfg.Control = fmt.Sprintf("http://%s:9876", host)
			}

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			ui.Ok("SSH %s:%d", cfg.SSHHost, cfg.SSHPort)
			ui.Ok("Mode: %s", mode)
			ui.Ok("Saved → %s", config.ClientConfigPath())
			return nil
		},
	}
}

func createCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create",
		Short: "Create a new sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client := newClient(cfg)

			ui.Info("Creating sandbox...")
			start := time.Now()

			sb, err := client.CreateSandbox("mob-sandbox:1.0")
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			// Wait for running state
			for i := 0; i < 30; i++ {
				s, err := client.GetSandbox(sb.ID)
				if err == nil && s.State == "started" || s.State == "running" {
					break
				}
				time.Sleep(2 * time.Second)
			}

			ui.Ok("%s ready (%s)", sb.ID, time.Since(start).Round(time.Second))
			return nil
		},
	}
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [id]",
		Short: "SSH into sandbox (creates new if no id)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client := newClient(cfg)

			var sandboxID string
			if len(args) > 0 {
				sandboxID = args[0]
			} else {
				ui.Info("Creating sandbox...")
				start := time.Now()
				sb, err := client.CreateSandbox("mob-sandbox:1.0")
				if err != nil {
					return fmt.Errorf("create: %w", err)
				}
				sandboxID = sb.ID
				for i := 0; i < 30; i++ {
					s, _ := client.GetSandbox(sandboxID)
					if s != nil && s.State == "started" || s.State == "running" {
						break
					}
					time.Sleep(2 * time.Second)
				}
				ui.Ok("%s ready (%s)", sandboxID, time.Since(start).Round(time.Second))
			}

			access, err := client.GetSSHAccess(sandboxID)
			if err != nil {
				return fmt.Errorf("ssh access: %w", err)
			}

			return remote.ConnectSandbox(cfg.SSHHost, cfg.SSHPort, access.Token)
		},
	}
}

func psCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List my sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client := newClient(cfg)

			sandboxes, err := client.ListSandboxes()
			if err != nil {
				return err
			}

			if len(sandboxes) == 0 {
				fmt.Println("  No sandboxes")
				return nil
			}

			fmt.Printf("  %-40s %s\n", "ID", "STATE")
			for _, sb := range sandboxes {
				fmt.Printf("  %-40s %s\n", sb.ID, sb.State)
			}
			return nil
		},
	}
}

func rmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			if err := newClient(cfg).DeleteSandbox(args[0]); err != nil {
				return err
			}
			ui.Ok("Deleted %s", args[0])
			return nil
		},
	}
}

func forwardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forward <id> <port>",
		Short: "SSH tunnel to localhost (Scheme A)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client := newClient(cfg)

			remotePort, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[1])
			}

			access, err := client.GetSSHAccess(args[0])
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()

			localPort, err := remote.PortForward(ctx, cfg.SSHHost, cfg.SSHPort, access.Token, remotePort)
			if err != nil {
				return err
			}

			ui.Ok("http://localhost:%d → sandbox:%d (Ctrl+C to stop)", localPort, remotePort)
			<-ctx.Done()
			return nil
		},
	}
}

func urlCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "url <id> <port>",
		Short: "Get preview URL (Scheme B, domain mode)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			if cfg.Mode == "ip" {
				return fmt.Errorf("preview URLs require domain mode — use 'mob forward' instead")
			}

			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[1])
			}

			// Extract domain from server URL
			domain := cfg.SSHHost
			serverURL := cfg.Server
			serverURL = strings.TrimPrefix(serverURL, "https://")
			serverURL = strings.TrimPrefix(serverURL, "http://")
			if strings.HasPrefix(serverURL, "daytona.") {
				domain = strings.TrimPrefix(serverURL, "daytona.")
			}
			if idx := strings.Index(domain, "/"); idx > 0 {
				domain = domain[:idx]
			}

			previewURL := newClient(cfg).BuildPreviewURL(args[0], port, domain)
			fmt.Printf("  %s  (auth via cookie, 1h)\n", previewURL)
			return nil
		},
	}
}

func exposeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "expose <id> <port> [name]",
		Short: "Permanent subdomain route (Scheme C, domain mode)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			if cfg.Mode == "ip" {
				return fmt.Errorf("expose requires domain mode")
			}

			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port: %s", args[1])
			}

			name := args[0][:8]
			if len(args) > 2 {
				name = args[2]
			}

			// Call control API
			body := fmt.Sprintf(`{"sandbox_id":"%s","port":%d,"name":"%s"}`, args[0], port, name)
			url := cfg.Control + "/control/v1/expose"

			resp, err := makeControlRequest("POST", url, body, cfg.APIKey)
			if err != nil {
				return err
			}
			fmt.Printf("  %s\n", resp)
			return nil
		},
	}
}

func openhandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "openhands",
		Short: "Open OpenHands in browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}

			url := cfg.OpenHands
			ui.Ok("Opening %s", url)

			var openCmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				openCmd = exec.Command("open", url)
			case "linux":
				openCmd = exec.Command("xdg-open", url)
			default:
				openCmd = exec.Command("cmd", "/c", "start", url)
			}
			return openCmd.Start()
		},
	}
}
