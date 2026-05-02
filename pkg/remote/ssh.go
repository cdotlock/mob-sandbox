package remote

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHClient struct {
	client *ssh.Client
}

func NewSSHClient(host string, port int, user, keyPath string) (*SSHClient, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config: ssh.Config{
			KeyExchanges: []string{"curve25519-sha256"},
		},
		Timeout: 10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return &SSHClient{client: client}, nil
}

func (c *SSHClient) Run(cmd string) (string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func (c *SSHClient) RunStream(cmd string, stdout, stderr io.Writer) error {
	sess, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	sess.Stdout = stdout
	sess.Stderr = stderr
	return sess.Run(cmd)
}

func (c *SSHClient) Upload(content []byte, remotePath string) error {
	sess, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	go func() {
		w, _ := sess.StdinPipe()
		defer w.Close()
		fmt.Fprintf(w, "C0644 %d %s\n", len(content), filepath.Base(remotePath))
		w.Write(content)
		fmt.Fprint(w, "\x00")
	}()

	dir := remotePath[:strings.LastIndex(remotePath, "/")]
	return sess.Run(fmt.Sprintf("mkdir -p %s && scp -t %s", dir, remotePath))
}

func (c *SSHClient) Close() error {
	return c.client.Close()
}

func ConnectSandbox(host string, port int, token string) error {
	config := &ssh.ClientConfig{
		User: token,
		Auth: []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config: ssh.Config{
			KeyExchanges: []string{"curve25519-sha256"},
		},
		Timeout: 10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	sess.Stdin = os.Stdin
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", 40, 120, modes); err != nil {
		return fmt.Errorf("request pty: %w", err)
	}
	if err := sess.Shell(); err != nil {
		return fmt.Errorf("shell: %w", err)
	}
	return sess.Wait()
}

func PortForward(ctx context.Context, host string, port int, token string, remotePort int) (int, error) {
	config := &ssh.ClientConfig{
		User: token,
		Auth: []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Config: ssh.Config{
			KeyExchanges: []string{"curve25519-sha256"},
		},
		Timeout: 10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return 0, fmt.Errorf("ssh dial: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		client.Close()
		return 0, fmt.Errorf("listen: %w", err)
	}

	localPort := listener.Addr().(*net.TCPAddr).Port

	go func() {
		defer client.Close()
		defer listener.Close()
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			remoteAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)
			remote, err := client.Dial("tcp", remoteAddr)
			if err != nil {
				local.Close()
				continue
			}
			go func() {
				defer local.Close()
				defer remote.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(remote, local); done <- struct{}{} }()
				go func() { io.Copy(local, remote); done <- struct{}{} }()
				select {
				case <-done:
				case <-ctx.Done():
				}
			}()
		}
	}()

	go func() {
		<-ctx.Done()
		listener.Close()
		client.Close()
	}()

	return localPort, nil
}
