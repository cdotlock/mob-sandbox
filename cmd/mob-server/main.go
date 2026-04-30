package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/control"
	"github.com/cdotlock/mob-sandbox/pkg/deploy"
	"github.com/cdotlock/mob-sandbox/pkg/guardian"
	"github.com/cdotlock/mob-sandbox/pkg/ui"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:     "mob-server",
		Short:   "mob-sandbox server daemon",
		Version: config.Version,
	}

	root.AddCommand(initCmd(), statusCmd(), keyCmd(), operatorCmd(), daemonCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func initCmd() *cobra.Command {
	var (
		domain      string
		dnsProvider string
		dnsToken    string
		dnsSecret   string
		llmKey      string
		llmURL      string
		llmModel    string
		sshKeyPath  string
		sshHost     string
		sshPort     int
		apiKey      string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap server — install all services",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.DefaultServerConfig()

			if domain != "" {
				cfg.Mode = "domain"
				cfg.Domain = domain
				cfg.DNS.Provider = dnsProvider
				cfg.DNS.Token = dnsToken
				cfg.DNS.Secret = dnsSecret
			}
			if llmKey != "" {
				cfg.LLM.APIKey = llmKey
			}
			if llmURL != "" {
				cfg.LLM.BaseURL = llmURL
			}
			if llmModel != "" {
				cfg.LLM.Model = llmModel
			}
			if apiKey != "" {
				cfg.APIKey = apiKey
			}

			if sshHost == "" {
				fmt.Print("  SSH host (server IP): ")
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					sshHost = strings.TrimSpace(scanner.Text())
				}
			}
			if sshKeyPath == "" {
				sshKeyPath = os.ExpandEnv("$HOME/.ssh/id_ed25519")
			}

			ui.Header("mob-server init")
			ui.Info("Mode: %s", cfg.Mode)
			if cfg.Mode == "domain" {
				ui.Info("Domain: %s", cfg.Domain)
			}

			sshClient, err := newSSHRunner(sshHost, sshPort, sshKeyPath)
			if err != nil {
				ui.Fatal("SSH connection failed: %v", err)
			}
			defer sshClient.Close()

			cfg.PublicIP = sshHost

			deployer := deploy.New(cfg, sshClient.Run, sshClient.Upload)
			if err := deployer.Run(); err != nil {
				return err
			}

			cfg.APIKey = deployer.GetAPIKey()
			cfg.PublicIP = deployer.GetPublicIP()
			if err := cfg.Save(); err != nil {
				ui.Warn("Failed to save config: %v", err)
			}

			ui.Header("Deployment Complete")
			fmt.Println()
			if cfg.Mode == "domain" {
				fmt.Printf("  Daytona:    https://daytona.%s\n", cfg.Domain)
				fmt.Printf("  OpenHands:  https://openhands.%s\n", cfg.Domain)
			} else {
				fmt.Printf("  Daytona:    http://%s:%d\n", cfg.PublicIP, cfg.Ports.API)
				fmt.Printf("  OpenHands:  http://%s:%d\n", cfg.PublicIP, cfg.Ports.OpenHands)
			}
			fmt.Printf("  SSH:        ssh -p %d <token>@%s\n", cfg.Ports.SSH, cfg.PublicIP)
			fmt.Printf("  API Key:    %s\n", cfg.APIKey)
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "", "Domain (enables TLS mode)")
	cmd.Flags().StringVar(&dnsProvider, "dns-provider", "manual", "DNS provider: cloudflare, porkbun, manual")
	cmd.Flags().StringVar(&dnsToken, "dns-token", "", "DNS API token")
	cmd.Flags().StringVar(&dnsSecret, "dns-secret", "", "DNS API secret (porkbun)")
	cmd.Flags().StringVar(&llmKey, "llm-key", "", "LLM API key (skip OpenHands if empty)")
	cmd.Flags().StringVar(&llmURL, "llm-url", "https://api.deepseek.com", "LLM base URL")
	cmd.Flags().StringVar(&llmModel, "llm-model", "deepseek-v4-pro", "LLM model name")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "Server SSH host/IP")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "Server SSH port")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Pre-generated Daytona API key")

	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service health and sandbox stats",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadServerConfig()
			if err != nil {
				return fmt.Errorf("load config: %w (run mob-server init first)", err)
			}

			mode := cfg.Mode
			if mode == "" {
				mode = "ip"
			}

			fmt.Printf("mob-sandbox · %s · %s mode\n\n", cfg.PublicIP, mode)

			containers := []string{
				"daytona-api", "daytona-runner", "daytona-proxy",
				"daytona-db", "daytona-redis", "daytona-registry",
				"daytona-minio", "daytona-ssh-gateway", "daytona-dex",
				"openhands",
			}

			for _, c := range containers {
				out, _ := localRun(fmt.Sprintf("docker inspect --format='{{.State.Running}}' %s 2>/dev/null || echo false", c))
				status := strings.TrimSpace(out)
				mark := "✓"
				if status != "true" {
					mark = "✗"
				}
				fmt.Printf("  %-20s %s\n", c, mark)
			}

			out, _ := localRun(`docker ps --filter "label=daytona.sandbox" --format "{{.ID}}" | wc -l`)
			fmt.Printf("\n  sandboxes: %s running\n", strings.TrimSpace(out))
			return nil
		},
	}
}

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage API keys",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "create <name>",
			Short: "Create a new API key",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				newKey := "mob-" + deploy.RandomHex(20)
				if err := insertAPIKeyLocal(args[0], newKey); err != nil {
					return err
				}
				fmt.Printf("  API Key: %s\n", newKey)
				fmt.Printf("  Name:    %s\n", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List API keys",
			RunE: func(cmd *cobra.Command, args []string) error {
				out, err := localRun(`docker exec daytona-db psql -U daytona -d daytona -t -c "SELECT name, \"keyPrefix\", \"createdAt\" FROM api_key ORDER BY \"createdAt\""`)
				if err != nil {
					return err
				}
				fmt.Println("  Name          | Prefix   | Created")
				fmt.Println("  --------------|----------|--------")
				for _, line := range strings.Split(out, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						fmt.Printf("  %s\n", line)
					}
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "revoke <name>",
			Short: "Revoke an API key",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				script := fmt.Sprintf(`docker exec daytona-db psql -U daytona -d daytona -c "DELETE FROM api_key WHERE name = '%s'"`, args[0])
				out, err := localRun(script)
				if err != nil {
					return err
				}
				fmt.Printf("  %s\n", strings.TrimSpace(out))
				return nil
			},
		},
	)

	return cmd
}

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run guardian daemon (foreground)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadServerConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			// Start control API in background
			if cfg.Mode == "domain" && cfg.Domain != "" {
				ctrlSrv := control.NewServer(cfg.Ports.Control, cfg.Domain)
				go ctrlSrv.Start()
			}

			g := guardian.New(localRun)
			g.Run()
			return nil
		},
	}
}
