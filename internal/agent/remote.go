package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
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
// If gatewayURL is non-empty, a reverse SSH tunnel is established so the remote
// agent reaches the gateway through the launching host.
func RemoteExec(logger *zap.Logger, remoteHost string, gatewayURL string, localOutputDir string, args []string) error {
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

	// Set up reverse tunnel for gateway if URL is provided
	if gatewayURL != "" {
		tunnelURL, cleanup, err := setupReverseTunnel(client, logger, gatewayURL)
		if err != nil {
			return fmt.Errorf("setup reverse tunnel: %w", err)
		}
		defer cleanup()

		// Rewrite --gateway-url in args to point through the tunnel
		args = rewriteArg(args, "--gateway-url", tunnelURL)
		logger.Info("Reverse tunnel established",
			zap.String("original_gateway", gatewayURL),
			zap.String("tunnel_gateway", tunnelURL))
	}

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

	// Use a fixed remote output directory; the agent will create the hostname
	// subdirectory inside it automatically.
	remoteOutputDir := "/tmp/jagr-output"
	args = rewriteArg(args, "--output-dir", remoteOutputDir)

	// Build remote command
	cmdParts := []string{remotePath}
	cmdParts = append(cmdParts, args...)
	remoteCmd := strings.Join(cmdParts, " ")

	logger.Info("Executing on remote host", zap.String("command", remoteCmd))

	execErr := execRemote(client, remoteCmd, logger)

	// Collect artifacts from remote host regardless of execution outcome
	if localOutputDir != "" {
		logger.Info("Collecting artifacts from remote host",
			zap.String("remote_dir", remoteOutputDir),
			zap.String("local_dir", localOutputDir))
		if err := scpDownloadDir(client, remoteOutputDir, localOutputDir, logger); err != nil {
			logger.Error("Failed to collect remote artifacts", zap.Error(err))
		}
	}

	return execErr
}

// setupReverseTunnel creates a reverse SSH tunnel so the remote agent can reach
// the gateway through the launching host. It returns the rewritten URL
// (e.g. "https://127.0.0.1:43210") and a cleanup function.
func setupReverseTunnel(client *ssh.Client, logger *zap.Logger, gatewayURL string) (string, func(), error) {
	parsed, err := url.Parse(gatewayURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse gateway URL %q: %w", gatewayURL, err)
	}

	// Resolve the gateway host:port that is reachable from the launching host
	gwHost := parsed.Host
	if !strings.Contains(gwHost, ":") {
		switch parsed.Scheme {
		case "https":
			gwHost += ":443"
		default:
			gwHost += ":80"
		}
	}

	// Open a listener on the remote side (port 0 = OS picks a free port)
	remoteListener, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("open remote listener: %w", err)
	}

	remoteAddr := remoteListener.Addr().String()
	logger.Info("Reverse tunnel listener opened on remote",
		zap.String("remote_addr", remoteAddr),
		zap.String("local_target", gwHost))

	// Proxy goroutine: accept connections on remote side, forward to local gateway
	go func() {
		for {
			remoteConn, err := remoteListener.Accept()
			if err != nil {
				return // listener closed
			}
			go forwardConnection(remoteConn, gwHost, logger)
		}
	}()

	// Build the tunnel URL preserving the original scheme and path
	tunnelURL := fmt.Sprintf("%s://%s", parsed.Scheme, remoteAddr)
	if parsed.Path != "" && parsed.Path != "/" {
		tunnelURL += parsed.Path
	}

	cleanup := func() {
		remoteListener.Close()
	}

	return tunnelURL, cleanup, nil
}

// forwardConnection proxies a single connection from the remote tunnel to the
// local gateway address.
func forwardConnection(remoteConn net.Conn, localTarget string, logger *zap.Logger) {
	defer remoteConn.Close()

	localConn, err := net.Dial("tcp", localTarget)
	if err != nil {
		logger.Warn("Failed to connect to local gateway for tunnel",
			zap.String("target", localTarget),
			zap.Error(err))
		return
	}
	defer localConn.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(localConn, remoteConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(remoteConn, localConn)
		done <- struct{}{}
	}()
	<-done
}

// rewriteArg replaces the value of a --flag=value style argument in args.
func rewriteArg(args []string, flag, newValue string) []string {
	prefix := flag + "="
	result := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			result[i] = prefix + newValue
		} else {
			result[i] = arg
		}
	}
	return result
}

// scpDownloadDir downloads all files from a remote directory to a local directory.
// It uses tar over SSH to transfer the directory contents.
func scpDownloadDir(client *ssh.Client, remoteDir, localDir string, logger *zap.Logger) error {
	// First check whether the remote directory exists and has any files.
	// The remote agent may not have produced file artifacts.
	checkSession, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session for check: %w", err)
	}
	if err := checkSession.Run(fmt.Sprintf("test -d %s && find %s -mindepth 1 -print -quit | grep -q .", remoteDir, remoteDir)); err != nil {
		checkSession.Close()
		logger.Info("Remote output directory is empty or missing, nothing to collect")
		return nil
	}
	checkSession.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	// Use tar to stream the remote directory; -C changes into the dir so
	// the archive contains relative paths.
	if err := session.Start(fmt.Sprintf("tar cf - -C %s .", remoteDir)); err != nil {
		return fmt.Errorf("start remote tar: %w", err)
	}

	if err := os.MkdirAll(localDir, 0700); err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}

	// Extract locally
	extractCmd := fmt.Sprintf("tar xf - -C %s", localDir)
	extract := execLocalCommand(extractCmd, stdout)
	if extract != nil {
		logger.Warn("Local tar extract failed, falling back to manual read", zap.Error(extract))
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("remote tar: %w", err)
	}

	return extract
}

// execLocalCommand runs a local shell command, piping r into its stdin.
func execLocalCommand(cmd string, r io.Reader) error {
	c := exec.Command("sh", "-c", cmd)
	c.Stdin = r
	c.Stderr = os.Stderr
	return c.Run()
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

	w, err := session.StdinPipe()
	if err != nil {
		return err
	}

	r, err := session.StdoutPipe()
	if err != nil {
		return err
	}

	var stderrBuf strings.Builder
	session.Stderr = &stderrBuf

	if err := session.Start("scp -t " + remotePath); err != nil {
		return fmt.Errorf("start scp: %w", err)
	}

	// Helper to read SCP ack byte (0 = OK, 1 = warning, 2 = fatal)
	ack := make([]byte, 1)
	readAck := func() error {
		if _, err := io.ReadFull(r, ack); err != nil {
			return fmt.Errorf("read ack: %w", err)
		}
		if ack[0] != 0 {
			return fmt.Errorf("scp server error (code %d): %s", ack[0], stderrBuf.String())
		}
		return nil
	}

	// Wait for initial ready ack
	if err := readAck(); err != nil {
		return err
	}

	// Send file header
	fmt.Fprintf(w, "C0755 %d %s\n", stat.Size(), filepath.Base(remotePath))
	if err := readAck(); err != nil {
		return err
	}

	// Send file contents
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("copy file data: %w", err)
	}

	// Send transfer complete
	fmt.Fprint(w, "\x00")
	if err := readAck(); err != nil {
		return err
	}

	w.Close()
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp session: %w; stderr: %s", err, stderrBuf.String())
	}
	return nil
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
