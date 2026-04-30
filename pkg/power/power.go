package power

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

type request struct {
	Action    string `json:"action"`
	Operator  string `json:"operator"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

func Call(workerURL, operator, keyPath, action string) (string, error) {
	if workerURL == "" {
		return "", fmt.Errorf("power worker URL not configured — run 'mob power init'")
	}
	if operator == "" {
		return "", fmt.Errorf("operator name not configured — run 'mob power init'")
	}
	if keyPath == "" {
		keyPath = os.ExpandEnv("$HOME/.ssh/id_ed25519")
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("read SSH private key %s: %w", keyPath, err)
	}

	parsed, err := ssh.ParseRawPrivateKey(keyData)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return "", fmt.Errorf("SSH key %s is passphrase-protected; mob power needs an unencrypted key (try `ssh-keygen -p -f %s` to remove the passphrase)", keyPath, keyPath)
		}
		return "", fmt.Errorf("parse SSH key %s: %w", keyPath, err)
	}

	priv, err := edPrivKey(parsed)
	if err != nil {
		return "", fmt.Errorf("%s: %w", keyPath, err)
	}

	timestamp := time.Now().Unix()
	msg := fmt.Sprintf("%s|%s|%d", action, operator, timestamp)
	sig := ed25519.Sign(priv, []byte(msg))

	bodyJSON, err := json.Marshal(request{
		Action:    action,
		Operator:  operator,
		Timestamp: timestamp,
		Signature: base64.StdEncoding.EncodeToString(sig),
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", workerURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call worker %s: %w", workerURL, err)
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("worker returned %d: %s", resp.StatusCode, string(out))
	}
	return string(out), nil
}

func edPrivKey(parsed any) (ed25519.PrivateKey, error) {
	switch k := parsed.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("only ed25519 SSH keys supported (got %T) — generate one with `ssh-keygen -t ed25519`", parsed)
	}
}
