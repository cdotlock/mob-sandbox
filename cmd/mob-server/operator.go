package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

const (
	authorizedKeysPath = "/root/.ssh/authorized_keys"
	operatorMarker     = "mob-operator:"
)

func operatorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Manage SSH operators (root login pubkeys)",
		Long: `Add or remove SSH operator pubkeys in /root/.ssh/authorized_keys.

Each operator generates their own keypair locally, sends their pubkey to
the admin, and the admin runs 'mob-server operator add' on the server.
Lines added by this command are tagged with a 'mob-operator:<name>'
comment so they can be listed and revoked cleanly.`,
	}
	cmd.AddCommand(operatorAddCmd(), operatorListCmd(), operatorRevokeCmd(), operatorWorkerConfigCmd())
	return cmd
}

func operatorAddCmd() *cobra.Command {
	var pubkeyFile, pubkeyStr string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add SSH pubkey for an operator (ed25519 only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !validOperatorName(name) {
				return fmt.Errorf("invalid name %q: must match [a-z0-9_-]+", name)
			}
			if pubkeyFile == "" && pubkeyStr == "" {
				return fmt.Errorf("provide --pubkey-file or --pubkey")
			}

			var pubkeyData []byte
			if pubkeyStr != "" {
				pubkeyData = []byte(pubkeyStr)
			} else {
				data, err := os.ReadFile(pubkeyFile)
				if err != nil {
					return fmt.Errorf("read pubkey file: %w", err)
				}
				pubkeyData = data
			}

			pub, _, _, _, err := ssh.ParseAuthorizedKey(pubkeyData)
			if err != nil {
				return fmt.Errorf("parse pubkey: %w", err)
			}
			if pub.Type() != "ssh-ed25519" {
				return fmt.Errorf("only ssh-ed25519 supported, got %s", pub.Type())
			}

			wire := pub.Marshal()
			if len(wire) < 32 {
				return fmt.Errorf("malformed ed25519 pubkey wire format")
			}
			rawPub := wire[len(wire)-32:]
			rawPubB64 := base64.StdEncoding.EncodeToString(rawPub)

			line := fmt.Sprintf("%s %s %s%s",
				pub.Type(),
				base64.StdEncoding.EncodeToString(wire),
				operatorMarker,
				name,
			)

			if err := appendOperatorLine(name, line); err != nil {
				return err
			}

			fmt.Printf("✓ Added operator %q to %s\n", name, authorizedKeysPath)
			fmt.Printf("  fingerprint: %s\n", ssh.FingerprintSHA256(pub))
			fmt.Println()
			fmt.Println("For Cloudflare Worker AUTHORIZED_PUBKEYS, add this entry:")
			fmt.Printf(`  {"name":"%s","pubkey_b64":"%s"}`+"\n", name, rawPubB64)
			fmt.Println()
			fmt.Println("Run 'mob-server operator worker-config' to print the full list.")
			return nil
		},
	}
	cmd.Flags().StringVarP(&pubkeyFile, "pubkey-file", "f", "", "Path to .pub file")
	cmd.Flags().StringVar(&pubkeyStr, "pubkey", "", "Pubkey string (alternative to --pubkey-file)")
	return cmd
}

func operatorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List SSH operators",
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := loadOperators()
			if err != nil {
				return err
			}
			if len(ops) == 0 {
				fmt.Println("(no operators)")
				return nil
			}
			fmt.Printf("  %-20s %s\n", "NAME", "FINGERPRINT")
			for _, o := range ops {
				fmt.Printf("  %-20s %s\n", o.Name, o.Fingerprint)
			}
			return nil
		},
	}
}

func operatorRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke an SSH operator (removes from authorized_keys)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			data, err := os.ReadFile(authorizedKeysPath)
			if err != nil {
				return fmt.Errorf("read authorized_keys: %w", err)
			}

			marker := operatorMarker + name
			var keep []string
			removed := 0
			for _, l := range strings.Split(string(data), "\n") {
				if strings.Contains(l, marker) {
					removed++
					continue
				}
				keep = append(keep, l)
			}
			if removed == 0 {
				return fmt.Errorf("no operator named %q in %s", name, authorizedKeysPath)
			}
			out := strings.Join(keep, "\n")
			if err := atomicWrite(authorizedKeysPath, []byte(out), 0600); err != nil {
				return err
			}
			fmt.Printf("✓ Removed %d line(s) for operator %q\n", removed, name)
			fmt.Println()
			fmt.Println("Don't forget to remove the matching entry from the Cloudflare Worker's")
			fmt.Println("AUTHORIZED_PUBKEYS list and redeploy.")
			return nil
		},
	}
}

func operatorWorkerConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worker-config",
		Short: "Print Cloudflare Worker AUTHORIZED_PUBKEYS JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := loadOperators()
			if err != nil {
				return err
			}
			fmt.Println("[")
			for i, o := range ops {
				comma := ","
				if i == len(ops)-1 {
					comma = ""
				}
				fmt.Printf(`  {"name":"%s","pubkey_b64":"%s"}%s`+"\n", o.Name, o.PubkeyB64, comma)
			}
			fmt.Println("]")
			return nil
		},
	}
}

type operatorEntry struct {
	Name        string
	Fingerprint string
	PubkeyB64   string
}

func loadOperators() ([]operatorEntry, error) {
	data, err := os.ReadFile(authorizedKeysPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ops []operatorEntry
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		idx := strings.Index(l, operatorMarker)
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(l[idx+len(operatorMarker):])
		if name == "" {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(l))
		if err != nil || pub.Type() != "ssh-ed25519" {
			continue
		}
		wire := pub.Marshal()
		rawPub := wire[len(wire)-32:]
		ops = append(ops, operatorEntry{
			Name:        name,
			Fingerprint: ssh.FingerprintSHA256(pub),
			PubkeyB64:   base64.StdEncoding.EncodeToString(rawPub),
		})
	}
	return ops, nil
}

func appendOperatorLine(name, line string) error {
	existing, err := os.ReadFile(authorizedKeysPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read authorized_keys: %w", err)
	}

	marker := operatorMarker + name
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.Contains(l, marker) {
			return fmt.Errorf("operator %q already exists; run 'mob-server operator revoke %s' first", name, name)
		}
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString(line)
	buf.WriteByte('\n')

	return atomicWrite(authorizedKeysPath, buf.Bytes(), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func validOperatorName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}
