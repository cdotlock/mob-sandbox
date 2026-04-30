package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/power"
	"github.com/cdotlock/mob-sandbox/pkg/ui"
	"github.com/spf13/cobra"
)

func powerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "power",
		Short: "VPS power control via Cloudflare Worker (start/stop/reboot/status)",
		Long: `Power-control the underlying Vultr VPS by sending an SSH-signed
request to a Cloudflare Worker that holds the Vultr API key.

The operator's SSH ed25519 private key signs each request; the Worker
verifies against an authorized pubkey list. The Vultr API key never
leaves Cloudflare.

First-time setup: 'mob power init' to set the Worker URL and operator
name. The admin must have run 'mob-server operator add <name>' on the
server and added the operator's pubkey to the Worker's
AUTHORIZED_PUBKEYS (see infra/power-worker/README.md).`,
	}
	cmd.AddCommand(
		powerInitCmd(),
		powerActionCmd("start", "Power on the VPS"),
		powerActionCmd("stop", "Power off the VPS"),
		powerActionCmd("reboot", "Reboot the VPS"),
		powerActionCmd("status", "Show VPS power status"),
	)
	return cmd
}

func powerActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   action,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("client not configured — run 'mob init' and 'mob power init' first")
			}
			out, err := power.Call(cfg.PowerWorkerURL, cfg.OperatorName, cfg.SSHKeyPath, action)
			if err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
}

func powerInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Configure power worker URL, operator name, and SSH key path",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadClientConfig()
			if err != nil {
				cfg = &config.ClientConfig{}
			}

			scanner := bufio.NewScanner(os.Stdin)

			fmt.Print("  ? Power Worker URL (e.g. https://mob-power.<sub>.workers.dev): ")
			if cfg.PowerWorkerURL != "" {
				fmt.Printf("\n    [current: %s, press enter to keep] ", cfg.PowerWorkerURL)
			}
			scanner.Scan()
			if v := strings.TrimSpace(scanner.Text()); v != "" {
				cfg.PowerWorkerURL = v
			}

			fmt.Print("  ? Operator name (matches mob-server operator add): ")
			if cfg.OperatorName != "" {
				fmt.Printf("\n    [current: %s, press enter to keep] ", cfg.OperatorName)
			}
			scanner.Scan()
			if v := strings.TrimSpace(scanner.Text()); v != "" {
				cfg.OperatorName = v
			}

			fmt.Print("  ? SSH private key path [default: ~/.ssh/id_ed25519]: ")
			if cfg.SSHKeyPath != "" {
				fmt.Printf("\n    [current: %s, press enter to keep] ", cfg.SSHKeyPath)
			}
			scanner.Scan()
			if v := strings.TrimSpace(scanner.Text()); v != "" {
				cfg.SSHKeyPath = v
			}

			if cfg.PowerWorkerURL == "" || cfg.OperatorName == "" {
				return fmt.Errorf("power_worker_url and operator_name are required")
			}

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			ui.Ok("Saved → %s", config.ClientConfigPath())
			ui.Info("Test with: mob power status")
			return nil
		},
	}
}
