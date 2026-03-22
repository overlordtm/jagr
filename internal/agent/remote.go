package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHHostConfig holds resolved SSH config for a host.
type SSHHostConfig struct {
	Hostname     string
	Port         string
	User         string
	IdentityFile string
}

// RemoteExec copies the running binary to a remote host via SSH and executes it
// with the provided args (which should already have --remote stripped).
func RemoteExec(logger *zap.Logger, remoteHost string, args []string) error {
	cfg, err := resolveSSHConfig(remoteHost)
	if err != nil {
		return fmt.Errorf("resolve ssh config for %s: %w", remoteHost, err)
	}

	logger.Info("Resolved SSH config",
		zap.String("host", remoteHost),
		zap.String("hostname", cfg.Hostname),
		zap.String("port", cfg.Port),
		zap.String("user", cfg.User),
		zap.String("identity_file", cfg.IdentityFile))

	clientConfig, err := buildSSHClientConfig(cfg)
	if err != nil {
		return fmt.Errorf("build ssh client config: %w", err)
	}

	addr := net.JoinHostPort(cfg.Hostname, cfg.Port)
	logger.Info("Connecting to remote host", zap.String("addr", addr))

	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer client.Close()

	// Get path to our own binary
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	remotePath := "/tmp/jagr-agent"

	logger.Info("Copying binary to remote host",
		zap.String("local", selfPath),
		zap.String("remote", remotePath))

	if err := scpCopy(client, selfPath, remotePath); err != nil {
		return fmt.Errorf("scp copy: %w", err)
	}

	// Build remote command
	cmdParts := []string{remotePath}
	cmdParts = append(cmdParts, args...)
	remoteCmd := strings.Join(cmdParts, " ")

	logger.Info("Executing on remote host", zap.String("command", remoteCmd))

	return execRemote(client, remoteCmd, logger)
}

// scpCopy copies a local file to the remote host using the SCP protocol over an SSH session.
func scpCopy(client *ssh.Client, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		fmt.Fprintf(w, "C0755 %d %s\n", stat.Size(), filepath.Base(remotePath))
		io.Copy(w, f)
		fmt.Fprint(w, "\x00")
	}()

	return session.Run("scp -t " + remotePath)
}

// execRemote runs a command on the remote host, streaming stdout/stderr to our own stdout/stderr.
func execRemote(client *ssh.Client, cmd string, logger *zap.Logger) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	if err := session.Run(cmd); err != nil {
		return fmt.Errorf("remote command failed: %w", err)
	}
	return nil
}

// buildSSHClientConfig creates an ssh.ClientConfig from the resolved host config.
func buildSSHClientConfig(cfg SSHHostConfig) (*ssh.ClientConfig, error) {
	var authMethods []ssh.AuthMethod

	// Try SSH agent first
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(conn)
			authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	// Try identity file
	if cfg.IdentityFile != "" {
		path := expandPath(cfg.IdentityFile)
		key, err := os.ReadFile(path)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(key)
			if err == nil {
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
		}
	}

	// Try default key paths as fallback
	if cfg.IdentityFile == "" {
		home, _ := os.UserHomeDir()
		for _, name := range []string{"id_rsa", "id_ed25519", "id_ecdsa"} {
			path := filepath.Join(home, ".ssh", name)
			key, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			signer, err := ssh.ParsePrivateKey(key)
			if err != nil {
				continue
			}
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH authentication methods available (no agent, no identity files)")
	}

	// Try known_hosts, fall back to insecure if not available
	var hostKeyCallback ssh.HostKeyCallback
	home, _ := os.UserHomeDir()
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	if cb, err := knownhosts.New(knownHostsPath); err == nil {
		hostKeyCallback = cb
	} else {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	return &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}, nil
}

// resolveSSHConfig parses ~/.ssh/config to resolve host settings.
func resolveSSHConfig(host string) (SSHHostConfig, error) {
	cfg := SSHHostConfig{
		Hostname: host,
		Port:     "22",
		User:     currentUser(),
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, nil // use defaults
	}

	configPath := filepath.Join(home, ".ssh", "config")
	f, err := os.Open(configPath)
	if err != nil {
		return cfg, nil // no config, use defaults
	}
	defer f.Close()

	parseSSHConfig(f, host, &cfg)
	return cfg, nil
}

// parseSSHConfig reads an SSH config file and applies matching Host block settings.
func parseSSHConfig(r io.Reader, targetHost string, cfg *SSHHostConfig) {
	scanner := bufio.NewScanner(r)
	inMatchingBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value := splitSSHConfigLine(line)
		if key == "" {
			continue
		}

		key = strings.ToLower(key)

		if key == "host" {
			inMatchingBlock = matchSSHHostPattern(value, targetHost)
			continue
		}

		if !inMatchingBlock {
			continue
		}

		// Only set if not already set by a previous matching block (first match wins in SSH)
		switch key {
		case "hostname":
			if cfg.Hostname == targetHost {
				cfg.Hostname = value
			}
		case "port":
			if cfg.Port == "22" {
				cfg.Port = value
			}
		case "user":
			if cfg.User == currentUser() {
				cfg.User = value
			}
		case "identityfile":
			if cfg.IdentityFile == "" {
				cfg.IdentityFile = value
			}
		}
	}
}

func splitSSHConfigLine(line string) (string, string) {
	// Handle both "Key=Value" and "Key Value" formats
	if idx := strings.IndexByte(line, '='); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 {
		parts = strings.SplitN(line, "\t", 2)
	}
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func matchSSHHostPattern(pattern, host string) bool {
	for _, p := range strings.Fields(pattern) {
		negate := false
		if strings.HasPrefix(p, "!") {
			negate = true
			p = p[1:]
		}
		// Convert SSH glob to regex
		re := "^" + regexp.QuoteMeta(p) + "$"
		re = strings.ReplaceAll(re, `\*`, ".*")
		re = strings.ReplaceAll(re, `\?`, ".")
		matched, _ := regexp.MatchString(re, host)
		if negate && matched {
			return false
		}
		if !negate && matched {
			return true
		}
	}
	return false
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "root"
}
