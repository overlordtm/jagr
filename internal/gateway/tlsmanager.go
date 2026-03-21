package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

func buildTLSConfig(cfg *models.ServerConfig, log *zap.Logger) (*tls.Config, error) {
	tlsCfg := cfg.TLS

	// 1. ACME enabled — use autocert
	if tlsCfg.ACME.Enabled {
		return buildACMETLSConfig(&tlsCfg, log)
	}

	// 2. Static cert/key paths provided — load them, or generate self-signed if files missing
	if tlsCfg.Cert != "" && tlsCfg.Key != "" {
		if fileExists(tlsCfg.Cert) && fileExists(tlsCfg.Key) {
			return loadStaticTLSConfig(&tlsCfg, log)
		}
		log.Info("Certificate files not found, generating self-signed certificates",
			zap.String("cert", tlsCfg.Cert),
			zap.String("key", tlsCfg.Key))
		if err := generateSelfSignedCert(tlsCfg.Cert, tlsCfg.Key, log); err != nil {
			return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
		}
		return loadStaticTLSConfig(&tlsCfg, log)
	}

	// 3. No TLS configuration at all — return nil (plain HTTP)
	return nil, nil
}

func buildACMETLSConfig(tlsCfg *models.TLSConfig, log *zap.Logger) (*tls.Config, error) {
	if len(tlsCfg.ACME.Domains) == 0 {
		return nil, fmt.Errorf("ACME enabled but no domains specified")
	}

	cacheDir := tlsCfg.ACME.CacheDir
	if cacheDir == "" {
		cacheDir = "/var/lib/jagr/acme"
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(tlsCfg.ACME.Domains...),
		Cache:      autocert.DirCache(cacheDir),
		Email:      tlsCfg.ACME.Email,
	}

	log.Info("ACME TLS enabled",
		zap.Strings("domains", tlsCfg.ACME.Domains),
		zap.String("email", tlsCfg.ACME.Email),
		zap.String("cache_dir", cacheDir))

	tc := m.TLSConfig()
	tc.MinVersion = tls.VersionTLS12
	return tc, nil
}

func loadStaticTLSConfig(tlsCfg *models.TLSConfig, log *zap.Logger) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(tlsCfg.Cert, tlsCfg.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	log.Info("Loaded static TLS certificates",
		zap.String("cert", tlsCfg.Cert),
		zap.String("key", tlsCfg.Key))
	return tc, nil
}

func generateSelfSignedCert(certPath, keyPath string, log *zap.Logger) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"JAGR Gateway (self-signed)"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Ensure parent directories exist
	for _, p := range []string{certPath, keyPath} {
		if dir := filepath.Dir(p); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}
	}

	certFile, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return err
	}

	log.Info("Generated self-signed certificate",
		zap.String("cert", certPath),
		zap.String("key", keyPath),
		zap.Duration("validity", 365*24*time.Hour))
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
