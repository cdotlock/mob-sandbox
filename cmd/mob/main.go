package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
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
		Use:          "mob",
		Short:        "mob-sandbox client CLI",
		Version:      config.Version,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context())
		},
	}

	root.AddCommand(
		tuiCmd(),
		initCmd(),
		createCmd(),
		sshCmd(),
		claudeCmd(),
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

func sandboxEnv(cfg *config.ClientConfig) map[string]string {
	env := map[string]string{
		"PATH":         "/usr/local/nvm/versions/node/v22.14.0/bin:/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games",
		"NVM_DIR":      "/usr/local/nvm",
		"NODE_VERSION": "22.14.0",
		"NODE_PATH":    "/usr/local/nvm/v22.14.0/lib/node_modules",
		"LANG":         "C.UTF-8",
		"LC_ALL":       "C.UTF-8",
	}
	for k, v := range cfg.ClaudeCodeEnv {
		if v != "" {
			env[k] = v
		}
	}
	return env
}

func isSandboxReady(state string) bool {
	return state == "started" || state == "running"
}

func waitSandboxReady(client *daytona.Client, sandboxID string) error {
	for i := 0; i < 60; i++ {
		s, err := client.GetSandbox(sandboxID)
		if err == nil && isSandboxReady(s.State) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("sandbox %s did not become ready", sandboxID)
}

func ensureSandboxReady(client *daytona.Client, sandboxID string) error {
	s, err := client.GetSandbox(sandboxID)
	if err != nil {
		return err
	}
	if isSandboxReady(s.State) {
		return nil
	}

	ui.Info("Starting sandbox %s (%s)...", sandboxID, s.State)
	if err := client.StartSandbox(sandboxID); err != nil {
		return fmt.Errorf("start sandbox: %w", err)
	}
	return waitSandboxReady(client, sandboxID)
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Connect to server, save config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInitInteractive(bufio.NewReader(os.Stdin))
		},
	}
}

func runInitInteractive(reader *bufio.Reader) error {
	server, err := promptRequired(reader, "Server URL", "")
	if err != nil {
		return err
	}
	apiKey, err := promptRequired(reader, "API Key", "")
	if err != nil {
		return err
	}

	client := daytona.NewClient(server, apiKey)
	if err := client.Health(); err != nil {
		ui.Fail("Connection failed: %v", err)
		return err
	}
	ui.Ok("Connected")

	host := server
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}
	if idx := strings.Index(host, "/"); idx > 0 {
		host = host[:idx]
	}

	mode := "ip"
	info, err := client.GetInfo()
	if err == nil {
		if m, ok := info["mode"].(string); ok {
			mode = m
		}
	}

	cfg := &config.ClientConfig{
		Server:  server,
		APIKey:  apiKey,
		SSHHost: host,
		SSHPort: 2222,
		Mode:    mode,
	}

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

			sb, err := client.CreateSandboxWithEnv("mob-sandbox:1.0", sandboxEnv(cfg))
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			if err := waitSandboxReady(client, sb.ID); err != nil {
				return err
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
				if err := ensureSandboxReady(client, sandboxID); err != nil {
					return err
				}
			} else {
				ui.Info("Creating sandbox...")
				start := time.Now()
				sb, err := client.CreateSandboxWithEnv("mob-sandbox:1.0", sandboxEnv(cfg))
				if err != nil {
					return fmt.Errorf("create: %w", err)
				}
				sandboxID = sb.ID
				if err := waitSandboxReady(client, sandboxID); err != nil {
					return err
				}
				ui.Ok("%s ready (%s)", sandboxID, time.Since(start).Round(time.Second))
			}

			access, err := client.GetSSHAccess(sandboxID)
			if err != nil {
				return fmt.Errorf("ssh access: %w", err)
			}

			ui.Ok("Remote sandbox %s", sandboxID)
			return remote.ConnectSandbox(cfg.SSHHost, cfg.SSHPort, access.Token)
		},
	}
}

func claudeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claude [id]",
		Short: "Run Claude Code inside a remote sandbox",
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
				if err := ensureSandboxReady(client, sandboxID); err != nil {
					return err
				}
			} else {
				ui.Info("Creating sandbox...")
				start := time.Now()
				sb, err := client.CreateSandboxWithEnv("mob-sandbox:1.0", sandboxEnv(cfg))
				if err != nil {
					return fmt.Errorf("create: %w", err)
				}
				sandboxID = sb.ID
				if err := waitSandboxReady(client, sandboxID); err != nil {
					return err
				}
				ui.Ok("%s ready (%s)", sandboxID, time.Since(start).Round(time.Second))
			}

			access, err := client.GetSSHAccess(sandboxID)
			if err != nil {
				return fmt.Errorf("ssh access: %w", err)
			}

			ui.Ok("Running Claude Code in remote sandbox %s", sandboxID)
			return remote.RunSandboxCommand(cfg.SSHHost, cfg.SSHPort, access.Token, "cd ~ && exec /usr/local/bin/claude")
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

			if err := ensureSandboxReady(client, args[0]); err != nil {
				return err
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

			previewURL := newClient(cfg).BuildPreviewURL(args[0], port, previewDomain(cfg))
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

			name := defaultExposeName(args[0])
			if len(args) > 2 {
				name = args[2]
			}

			body, err := json.Marshal(map[string]any{
				"sandbox_id": args[0],
				"port":       port,
				"name":       name,
			})
			if err != nil {
				return err
			}
			url := cfg.Control + "/control/v1/expose"

			resp, err := makeControlRequest("POST", url, string(body), cfg.APIKey)
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
			return openBrowser(url)
		},
	}
}
