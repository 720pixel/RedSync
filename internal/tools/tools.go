// Package tools finds and runs the external binaries we lean on.
//
// dovi_tool and hdr10plus_tool ship inside the RedSync binary (they're niche and
// a pain to get), so on first use we drop them into a cache dir and run from
// there. ffmpeg / mkvtoolnix / mediainfo are expected on PATH or in a bundle dir
// next to the executable. Nothing here ever needs root.
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// names of the helpers we call. windows variants get .exe tacked on in resolve().
const (
	FFmpeg   = "ffmpeg"
	FFprobe  = "ffprobe"
	MkvMerge = "mkvmerge"
	MkvExtr  = "mkvextract"
	MkvProp  = "mkvpropedit"
	Mediainf = "mediainfo"
	DoviTool = "dovi_tool"
	Hdr10Plus = "hdr10plus_tool"
)

var (
	cacheOnce sync.Once
	cacheDir  string
	cacheErr  error
	resolved  sync.Map // name -> path, so we only look each binary up once
)

// CacheDir is where embedded helpers get extracted. user-writable, no sudo.
func CacheDir() (string, error) {
	cacheOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		cacheDir = filepath.Join(base, "redsync", "bin")
		cacheErr = os.MkdirAll(cacheDir, 0o755)
	})
	return cacheDir, cacheErr
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Path returns a runnable path for a helper, resolving in this order:
//  1. the embedded copy (dovi_tool / hdr10plus_tool only), extracted to cache
//  2. a bundle dir sitting next to the redsync executable
//  3. whatever's on PATH
func Path(name string) (string, error) {
	if v, ok := resolved.Load(name); ok {
		return v.(string), nil
	}
	p, err := resolve(name)
	if err == nil {
		resolved.Store(name, p)
	}
	return p, err
}

func resolve(name string) (string, error) {
	if p, ok := locate(name); ok {
		return p, nil
	}
	// last resort: fetch it ourselves if we know how (ffmpeg / ffprobe)
	if provisionable(name) {
		if p, err := provision(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found: %s", name, installHint(name))
}

// locate finds a tool without downloading anything: bundled copy, a vendor/
// folder next to the binary, the provisioned ffmpeg cache, then PATH.
func locate(name string) (string, bool) {
	// bundled tools first - guarantees the version we shipped, not a stale system one
	if isBundled(name) {
		if p, err := extractBundled(name); err == nil {
			return p, true
		}
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "vendor", exeName(name))
		if isRunnable(cand) {
			return cand, true
		}
	}
	if provisionable(name) {
		if dir, err := CacheDir(); err == nil {
			cand := filepath.Join(filepath.Dir(dir), "ffmpeg", exeName(name))
			if isRunnable(cand) {
				return cand, true
			}
		}
	}
	if p, err := exec.LookPath(exeName(name)); err == nil {
		return p, true
	}
	return "", false
}

// Locate reports where a tool is, or false if it's missing (no download). Used
// by `doctor`.
func Locate(name string) (string, bool) { return locate(name) }

// Hint returns install guidance for a tool.
func Hint(name string) string { return installHint(name) }

// Required is the full set of external tools RedSync can use.
func Required() []string {
	return []string{FFmpeg, FFprobe, MkvMerge, MkvExtr, MkvProp, Mediainf, DoviTool, Hdr10Plus}
}

// Bundled reports whether a tool ships inside the binary.
func Bundled(name string) bool { return isBundled(name) }

func isRunnable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// Cmd builds an *exec.Cmd for a helper, returning a clear error if it's missing.
func Cmd(name string, args ...string) (*exec.Cmd, error) {
	p, err := Path(name)
	if err != nil {
		return nil, err
	}
	return exec.Command(p, args...), nil
}
