package offset

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/720pixel/RedSync/internal/tools"
)

// Decode pulls a mono f32 signal out of a file at our working sample rate,
// straight from ffmpeg's stdout. No temp wav on disk - that's most of the speed
// win over the old shell scripts, which wrote ac3 then wav then read them back.
//
// start/dur are in seconds. dur <= 0 means "to the end".
func Decode(ctx context.Context, path string, trackIdx int, start, dur float64) ([]float64, error) {
	args := []string{"-v", "quiet", "-nostdin"}
	if start > 0 {
		args = append(args, "-ss", trimFmt(start))
	}
	if dur > 0 {
		args = append(args, "-t", trimFmt(dur))
	}
	args = append(args,
		"-i", path,
		"-map", fmt.Sprintf("0:%d", trackIdx),
		"-ac", "1",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-f", "f32le",
		"-c:a", "pcm_f32le",
		"-",
	)
	cmd, err := tools.Cmd(tools.FFmpeg, args...)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return f32leToFloats(buf.Bytes()), nil
}

func f32leToFloats(b []byte) []float64 {
	n := len(b) / 4
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = float64(math.Float32frombits(bits))
	}
	return out
}

func trimFmt(sec float64) string {
	return fmt.Sprintf("%.3f", sec)
}

// drain is here so callers can throw away a reader without leaking. small, but
// handy when wiring pipes.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
