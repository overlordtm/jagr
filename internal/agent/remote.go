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

	// Copy our binary to the remote host first — the stdio tunnel fallback
	// needs to invoke it before the main agent command runs.
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return fmt.Errorf("eval symlinks: %w", err)
	}

	logger.Info("Copying binary to remote host", zap.String("local", selfPath))

	remotePath, err := scpCopy(client, selfPath)
	if err != nil {
		return fmt.Errorf("scp copy: %w", err)
	}
	logger.Info("Binary copied to remote host", zap.String("remote", remotePath))

	// Set up a tunnel so the remote agent can reach the gateway.
	if gatewayURL != "" {
		tunnelURL, cleanup, err := setupReverseTunnel(client, logger, gatewayURL, remotePath)
		if err != nil {
			return fmt.Errorf("setup gateway tunnel: %w", err)
		}
		defer cleanup()

		// Rewrite --gateway-url in args to point through the tunnel
		args = rewriteArg(args, "--gateway-url", tunnelURL)
		logger.Info("Gateway tunnel established",
			zap.String("original_gateway", gatewayURL),
			zap.String("tunnel_gateway", tunnelURL))
	}

	// Use a fixed remote output directory; the agent will create the hostname
	// subdirectory inside it automatically.
	remoteOutputDir := "/tmp/jagr-output"
	args = rewriteArg(args, "--output-dir", remoteOutputDir)

	// Build remote command
	var cmdParts []string
	if cfg.User != "root" {
		cmdParts = append(cmdParts, "sudo")
	}
	cmdParts = append(cmdParts, remotePath)
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

// setupReverseTunnel creates a tunnel so the remote agent can reach the gateway
// through the launching host. It first tries an SSH reverse tunnel (tcpip-forward);
// if the server denies that, it falls back to a local listener on the interface
// the SSH connection uses (the remote agent then connects directly to that IP).
func setupReverseTunnel(client *ssh.Client, logger *zap.Logger, gatewayURL string, remoteBinPath string) (string, func(), error) {
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

	// Try reverse tunnel: SSH server on remote listens and forwards here.
	remoteListener, err := client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// tcpip-forward denied (AllowTcpForwarding disabled on the server).
		// Fall back: bind a local listener and have the remote agent connect
		// to our IP directly.
		logger.Warn("Remote TCP forwarding denied by SSH server, falling back to local listener",
			zap.Error(err))
		return setupLocalListenerFallback(client, logger, parsed, gwHost, remoteBinPath)
	}

	remoteAddr := remoteListener.Addr().String()
	logger.Info("Reverse tunnel listener opened on remote",
		zap.String("remote_addr", remoteAddr),
		zap.String("local_target", gwHost))

	go func() {
		for {
			remoteConn, err := remoteListener.Accept()
			if err != nil {
				return // listener closed
			}
			go forwardConnection(remoteConn, gwHost, logger)
		}
	}()

	tunnelURL := fmt.Sprintf("%s://%s", parsed.Scheme, remoteAddr)
	if parsed.Path != "" && parsed.Path != "/" {
		tunnelURL += parsed.Path
	}

	return tunnelURL, func() { remoteListener.Close() }, nil
}

// setupLocalListenerFallback binds a TCP listener on the local interface used
// by the SSH connection. The remote agent connects to that IP:port directly
// (not through the SSH channel) and traffic is proxied to the gateway.
// This requires the launching host to be TCP-reachable from the remote on the
// chosen port, but does not require AllowTcpForwarding on the SSH server.
func setupLocalListenerFallback(client *ssh.Client, logger *zap.Logger, parsed *url.URL, gwHost string, remoteBinPath string) (string, func(), error) {
	localIP, _, err := net.SplitHostPort(client.LocalAddr().String())
	if err != nil {
		return "", nil, fmt.Errorf("determine local IP from SSH connection: %w", err)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(localIP, "0"))
	if err != nil {
		return "", nil, fmt.Errorf("local fallback listener: %w", err)
	}

	logger.Info("Local fallback listener opened",
		zap.String("local_addr", ln.Addr().String()),
		zap.String("local_target", gwHost))

	// Verify the remote can actually reach our local IP.  We do this by
	// checking whether the remote has a route to our address.  If not, fall
	// through to the stdio tunnel which works over any topology.
	if !remoteCanReach(client, localIP) {
		ln.Close()
		logger.Warn("Remote has no route to local IP, falling back to stdio tunnel",
			zap.String("local_ip", localIP))
		return setupStdioTunnel(client, logger, parsed, gwHost, remoteBinPath)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go forwardConnection(conn, gwHost, logger)
		}
	}()

	tunnelURL := fmt.Sprintf("%s://%s", parsed.Scheme, ln.Addr().String())
	if parsed.Path != "" && parsed.Path != "/" {
		tunnelURL += parsed.Path
	}

	return tunnelURL, func() { ln.Close() }, nil
}

// remoteCanReach returns true when the remote host has a route to addr.
func remoteCanReach(client *ssh.Client, addr string) bool {
	session, err := client.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()
	// "ip route get" exits non-zero when there is no route.
	err = session.Run(fmt.Sprintf("ip route get %s", addr))
	return err == nil
}

// setupStdioTunnel starts the already-copied jagr binary on the remote host in
// hidden stdio-tunnel mode via a second SSH exec session. The remote binary
// listens on a loopback port and multiplexes connections back to us over the
// session's stdin/stdout. We proxy those streams to the local gateway.
//
// This works regardless of network topology or AllowTcpForwarding settings
// because it reuses the existing SSH TCP connection as the transport.
func setupStdioTunnel(client *ssh.Client, logger *zap.Logger, parsed *url.URL, gwHost string, remoteBinPath string) (string, func(), error) {
	const tunnelLoopbackPort = 17537

	session, err := client.NewSession()
	if err != nil {
		return "", nil, fmt.Errorf("new stdio-tunnel session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return "", nil, fmt.Errorf("stdio-tunnel stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return "", nil, fmt.Errorf("stdio-tunnel stdout pipe: %w", err)
	}

	cmd := fmt.Sprintf("%s --stdio-tunnel=127.0.0.1:%d", remoteBinPath, tunnelLoopbackPort)
	if err := session.Start(cmd); err != nil {
		session.Close()
		return "", nil, fmt.Errorf("start stdio-tunnel command: %w", err)
	}

	// Wait for the server's ready byte before returning, so the remote
	// listener is up by the time the remote agent tries to connect.
	ready := make([]byte, 1)
	if _, err := io.ReadFull(stdout, ready); err != nil || ready[0] != tunnelReady {
		session.Close()
		return "", nil, fmt.Errorf("stdio-tunnel server did not signal ready")
	}

	go RunStdioTunnelClient(stdout, stdin, gwHost, logger)

	tunnelURL := fmt.Sprintf("%s://127.0.0.1:%d", parsed.Scheme, tunnelLoopbackPort)
	if parsed.Path != "" && parsed.Path != "/" {
		tunnelURL += parsed.Path
	}

	logger.Info("Stdio tunnel established",
		zap.String("remote_bin", remoteBinPath),
		zap.Int("loopback_port", tunnelLoopbackPort),
		zap.String("tunnel_url", tunnelURL))

	return tunnelURL, func() { session.Close() }, nil
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

// scpCopy copies a local file to the remote host by streaming it through an SSH session.
// It first runs mktemp on the remote to obtain a unique path, then streams the file there.
// Returns the remote path chosen by mktemp.
// Uses cat + chmod instead of the legacy SCP protocol, which is no longer available
// on newer OpenSSH servers (9.0+).
func scpCopy(client *ssh.Client, localPath string) (string, error) {
	mktempSession, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session for mktemp: %w", err)
	}
	out, err := mktempSession.Output("mktemp /tmp/jagr-XXXXXX")
	mktempSession.Close()
	if err != nil {
		return "", fmt.Errorf("mktemp on remote: %w", err)
	}
	remotePath := strings.TrimSpace(string(out))

	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	w, err := session.StdinPipe()
	if err != nil {
		return "", err
	}

	var stderrBuf strings.Builder
	session.Stderr = &stderrBuf

	cmd := fmt.Sprintf("cat > %s && chmod 755 %s", remotePath, remotePath)
	if err := session.Start(cmd); err != nil {
		return "", fmt.Errorf("start remote copy: %w", err)
	}

	if _, err := io.Copy(w, f); err != nil {
		return "", fmt.Errorf("copy file data: %w", err)
	}
	w.Close()

	if err := session.Wait(); err != nil {
		return "", fmt.Errorf("remote copy: %w; stderr: %s", err, stderrBuf.String())
	}
	return remotePath, nil
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

// parseRemoteTarget splits a --remote value of the form [user@]host[:port]
// into its components. The host part is used for SSH config lookup.
func parseRemoteTarget(target string) (user, host, port string) {
	// Extract optional user@ prefix
	if idx := strings.LastIndex(target, "@"); idx >= 0 {
		user = target[:idx]
		target = target[idx+1:]
	}

	// Extract optional :port suffix, but only when target is not an IPv6
	// address already wrapped in brackets (e.g. [::1]:22 is handled by
	// net.SplitHostPort; bare ::1 has no port).
	if strings.HasPrefix(target, "[") {
		// Bracketed IPv6: delegate to net.SplitHostPort
		h, p, err := net.SplitHostPort(target)
		if err == nil {
			host = h
			port = p
			return
		}
	} else if idx := strings.LastIndex(target, ":"); idx >= 0 {
		// Make sure it's not a bare IPv6 address (multiple colons → no port)
		if strings.Count(target, ":") == 1 {
			host = target[:idx]
			port = target[idx+1:]
			return
		}
	}

	host = target
	return
}

// resolveSSHConfig parses ~/.ssh/config to resolve host settings.
// target may be [user@]host[:port]; inline values override SSH config.
func resolveSSHConfig(target string) (SSHHostConfig, error) {
	user, host, port := parseRemoteTarget(target)

	cfg := SSHHostConfig{
		Hostname: host,
		Port:     "22",
		User:     currentUser(),
	}

	home, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(home, ".ssh", "config")
		f, err := os.Open(configPath)
		if err == nil {
			parseSSHConfig(f, host, &cfg)
			f.Close()
		}
	}

	// Inline user@ and :port override whatever SSH config resolved
	if user != "" {
		cfg.User = user
	}
	if port != "" {
		cfg.Port = port
	}

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
