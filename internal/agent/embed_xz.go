//go:build xzcompress

package agent

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/ulikunitz/xz"
)

//go:embed tools/busybox.xz tools/linpeas.sh.xz tools/pspy.xz tools/pspy64.xz
var toolsFS embed.FS

func extractFile(f embed.FS, srcPath, dstPath string) error {
	compressed, err := f.Open(srcPath + ".xz")
	if err != nil {
		return err
	}
	defer compressed.Close()

	reader, err := xz.NewReader(compressed)
	if err != nil {
		return err
	}

	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return err
	}

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = out.ReadFrom(reader)
	return err
}

func computeEmbedHash(f embed.FS, path string) (string, error) {
	compressed, err := f.Open(path + ".xz")
	if err != nil {
		return "", err
	}
	defer compressed.Close()

	reader, err := xz.NewReader(compressed)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		return "", err
	}

	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}
