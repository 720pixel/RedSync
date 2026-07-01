package tools

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ffmpeg and ffprobe are too big to bake into the binary, so when they're not
// found anywhere we fetch a static build into the cache once, on first use. The
// binary stays small and the user still never has to install ffmpeg by hand.

const (
	ffmpegLinux   = "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz"
	ffmpegWindows = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip"
)

// provisionable reports whether we know how to auto-fetch a tool.
func provisionable(name string) bool {
	return name == FFmpeg || name == FFprobe
}

// provision returns a path to name, downloading the ffmpeg bundle if needed.
func provision(name string) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	ffDir := filepath.Join(filepath.Dir(dir), "ffmpeg")
	dst := filepath.Join(ffDir, exeName(name))
	if isRunnable(dst) {
		return dst, nil
	}
	if err := os.MkdirAll(ffDir, 0o755); err != nil {
		return "", err
	}

	fmt.Fprintln(os.Stderr, "fetching ffmpeg (one time, ~30 MB)...")
	if runtime.GOOS == "windows" {
		err = fetchFFmpegZip(ffmpegWindows, ffDir)
	} else {
		err = fetchFFmpegTarXz(ffmpegLinux, ffDir)
	}
	if err != nil {
		return "", fmt.Errorf("could not fetch ffmpeg automatically: %w", err)
	}
	if !isRunnable(dst) {
		return "", fmt.Errorf("ffmpeg download did not contain %s", exeName(name))
	}
	return dst, nil
}

// fetchFFmpegTarXz streams the linux static tarball and pulls ffmpeg + ffprobe
// out of it using the system tar (every linux has it, and it speaks xz).
func fetchFFmpegTarXz(url, dir string) error {
	tmp := filepath.Join(dir, "ffmpeg.tar.xz")
	if err := download(url, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)

	// extract just the two binaries, flattening the leading version folder
	cmd := exec.Command("tar", "-xJf", tmp, "-C", dir, "--strip-components=1", "--wildcards", "*/ffmpeg", "*/ffprobe")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	for _, n := range []string{"ffmpeg", "ffprobe"} {
		_ = os.Chmod(filepath.Join(dir, n), 0o755)
	}
	return nil
}

// fetchFFmpegZip pulls ffmpeg.exe + ffprobe.exe out of the windows build zip.
func fetchFFmpegZip(url, dir string) error {
	tmp := filepath.Join(dir, "ffmpeg.zip")
	if err := download(url, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)

	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if base != "ffmpeg.exe" && base != "ffprobe.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func download(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// installHint tells the user how to get a tool we can't fetch for them.
func installHint(name string) string {
	switch name {
	case MkvMerge, MkvExtr, MkvProp:
		if runtime.GOOS == "windows" {
			return "install MKVToolNix: winget install MoritzBunkus.MKVToolNix"
		}
		return "install MKVToolNix: apt install mkvtoolnix  (or your package manager)"
	case Mediainf:
		if runtime.GOOS == "windows" {
			return "install MediaInfo CLI: winget install MediaArea.MediaInfo"
		}
		return "install MediaInfo CLI: apt install mediainfo"
	case FFmpeg, FFprobe:
		return "ffmpeg is fetched automatically on first run; or install it yourself and put it on PATH"
	}
	return "put " + strings.ToLower(name) + " on your PATH"
}
