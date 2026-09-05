// Package subtitle reads, aligns and writes standalone text subtitle files.
package subtitle

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/720pixel/RedSync/internal/tools"
)

// Cue is a timed subtitle event. Text intentionally stays format-neutral so a
// sync can convert between SRT and WebVTT without bringing in a large parser.
type Cue struct {
	Start time.Duration
	End   time.Duration
	Text  []string
}

var textExtensions = map[string]bool{
	".srt": true, ".vtt": true, ".webvtt": true,
	".ass": true, ".ssa": true, ".sbv": true, ".sub": true,
	".ttml": true, ".dfxp": true, ".itt": true, ".smi": true, ".sami": true,
	".stl": true, ".scc": true, ".mpl": true, ".mpl2": true, ".jss": true,
	".aqt": true, ".pjs": true, ".rt": true,
}

func IsTextExtension(path string) bool {
	return textExtensions[strings.ToLower(filepath.Ext(path))]
}

// Read accepts SRT and WebVTT directly. Other FFmpeg-readable text subtitle
// formats are normalized to WebVTT through an in-memory pipe.
func Read(ctx context.Context, path string) ([]Cue, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".srt" || ext == ".vtt" || ext == ".webvtt" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseTimedText(b)
	}
	cmd, err := tools.CmdContext(ctx, tools.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", path, "-map", "0:s:0", "-f", "webvtt", "pipe:1",
	)
	if err != nil {
		return nil, err
	}
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("convert subtitle %s to WebVTT: %w", filepath.Base(path), err)
	}
	return parseTimedText(b)
}

var timingLine = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{2}[\.,]\d{3})\s*-->\s*((?:\d+:)?\d{1,2}:\d{2}[\.,]\d{3})(?:\s+.*)?$`)

func parseTimedText(data []byte) ([]Cue, error) {
	// Windows subtitle releases commonly use BOM-marked UTF-16. Decode it
	// explicitly so a supported text file does not become an empty cue set.
	if len(data) >= 2 && ((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		var order binary.ByteOrder = binary.LittleEndian
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		if (len(data)-2)%2 != 0 {
			return nil, fmt.Errorf("truncated UTF-16 subtitle")
		}
		units := make([]uint16, (len(data)-2)/2)
		for i := range units {
			units[i] = order.Uint16(data[2+i*2:])
		}
		for i := 0; i < len(units); i++ {
			if units[i] >= 0xd800 && units[i] <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					return nil, fmt.Errorf("invalid UTF-16 subtitle surrogate pair")
				}
				i++
			} else if units[i] >= 0xdc00 && units[i] <= 0xdfff {
				return nil, fmt.Errorf("invalid UTF-16 subtitle surrogate pair")
			}
		}
		data = []byte(string(utf16.Decode(units)))
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("subtitle text is not valid UTF-8 or BOM-marked UTF-16")
	}
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	var cues []Cue
	for i := 0; i < len(lines); i++ {
		m := timingLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		start, err := parseTimestamp(m[1])
		if err != nil {
			return nil, err
		}
		end, err := parseTimestamp(m[2])
		if err != nil {
			return nil, err
		}
		var text []string
		for i++; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			if timingLine.MatchString(lines[i]) {
				i-- // Missing blank separator: let the outer loop parse this cue.
				break
			}
			if i+1 < len(lines) && timingLine.MatchString(lines[i+1]) {
				if _, err := strconv.Atoi(strings.TrimSpace(lines[i])); err == nil {
					continue
				}
			}
			text = append(text, lines[i])
		}
		if end > start {
			cues = append(cues, Cue{Start: start, End: end, Text: text})
		}
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no timed text cues found")
	}
	// ASS layers and hand-edited SRTs need not arrive in chronological order.
	// Downstream monotonic matching requires time order; keep overlaps intact.
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
	return cues, nil
}

func parseTimestamp(s string) (time.Duration, error) {
	s = strings.ReplaceAll(s, ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid subtitle timestamp %q", s)
	}
	var h int64
	if len(parts) == 3 {
		var err error
		h, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid subtitle timestamp %q", s)
		}
		parts = parts[1:]
	}
	m, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid subtitle timestamp %q", s)
	}
	seconds, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid subtitle timestamp %q", s)
	}
	return time.Duration((float64(h*3600+m*60)+seconds)*float64(time.Second) + 0.5), nil
}

// Write uses native writers for VTT/SRT. Other requested extensions are handed
// to FFmpeg as WebVTT input, which covers ASS/SSA/TTML and other writable text
// formats without adding a runtime dependency.
func Write(ctx context.Context, path string, cues []Cue, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("output already exists: %s (use --overwrite to replace it)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(path))
	var data []byte
	switch ext {
	case ".vtt", ".webvtt":
		data = marshalVTT(cues)
	case ".srt":
		data = marshalSRT(cues)
	default:
		data = marshalVTT(cues)
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
		if overwrite {
			args = append(args, "-y")
		} else {
			args = append(args, "-n")
		}
		args = append(args, "-f", "webvtt", "-i", "pipe:0", path)
		cmd, err := tools.CmdContext(ctx, tools.FFmpeg, args...)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("write subtitle %s: %w", filepath.Base(path), err)
		}
		return nil
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func marshalVTT(cues []Cue) []byte {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		fmt.Fprintf(&b, "%s --> %s\n", formatTimestamp(c.Start, '.'), formatTimestamp(c.End, '.'))
		for _, line := range c.Text {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func marshalSRT(cues []Cue) []byte {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, formatTimestamp(c.Start, ','), formatTimestamp(c.End, ','))
		for _, line := range c.Text {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func formatTimestamp(d time.Duration, decimal byte) string {
	if d < 0 {
		d = 0
	}
	ms := d.Round(time.Millisecond).Milliseconds()
	h := ms / 3_600_000
	ms %= 3_600_000
	m := ms / 60_000
	ms %= 60_000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, decimal, ms)
}
