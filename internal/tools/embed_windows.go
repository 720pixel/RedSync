//go:build windows

package tools

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed assets/windows/dovi_tool.exe
var doviBin []byte

//go:embed assets/windows/hdr10plus_tool.exe
var hdr10Bin []byte

func bundledBytes(name string) ([]byte, bool) {
	switch name {
	case DoviTool:
		return doviBin, true
	case Hdr10Plus:
		return hdr10Bin, true
	}
	return nil, false
}

func isBundled(name string) bool {
	_, ok := bundledBytes(name)
	return ok
}

func extractBundled(name string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, exeName(name))
	if isRunnable(dst) {
		return dst, nil
	}
	data, _ := bundledBytes(name)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}
