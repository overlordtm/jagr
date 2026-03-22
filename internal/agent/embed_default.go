//go:build !xzcompress

package agent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

//go:embed tools/busybox tools/linpeas.sh tools/pspy tools/pspy64
var toolsFS embed.FS

func extractFile(f embed.FS, srcPath, dstPath string) error {
	data, err := f.ReadFile(srcPath)
	if err != nil {
		return err
	}

	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return err
	}

	return os.WriteFile(dstPath, data, 0755)
}

func computeEmbedHash(f embed.FS, path string) (string, error) {
	data, err := f.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
