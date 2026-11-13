package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/720pixel/RedSync/internal/tools"
	"github.com/720pixel/RedSync/internal/ui"
)

// Local avmux builds only (see tools/muxer_avmux.go). mkvmerge shifts chapters
// during the mux with --chapter-sync; avmux can't, so the chapters are copied
// unshifted and moved afterwards: mkvextract pulls the source's XML chapters,
// every timestamp moves by the measured delay, and mkvpropedit writes them onto
// the output. XML keeps languages, editions and flags intact.

// avmuxChapterShift returns the contribution whose chapters still need moving.
// At most one source provides chapters.
func avmuxChapterShift(j Job) (Contribution, bool) {
	if !tools.LocalAvmux {
		return Contribution{}, false
	}
	for _, c := range j.Contribs {
		if c.Chapters && c.File.HasChapters && c.Drift.DelayMS != 0 {
			return c, true
		}
	}
	return Contribution{}, false
}

var chapterTime = regexp.MustCompile(`<(ChapterTimeStart|ChapterTimeEnd)>(\d+):(\d{2}):(\d{2})\.(\d{9})</`)

// shiftChapterXML moves every chapter start/end by delayMS, clamping at zero.
func shiftChapterXML(xml string, delayMS int) string {
	return chapterTime.ReplaceAllStringFunc(xml, func(m string) string {
		p := chapterTime.FindStringSubmatch(m)
		h, _ := strconv.ParseInt(p[2], 10, 64)
		mi, _ := strconv.ParseInt(p[3], 10, 64)
		se, _ := strconv.ParseInt(p[4], 10, 64)
		ns, _ := strconv.ParseInt(p[5], 10, 64)
		t := ((h*60+mi)*60+se)*1_000_000_000 + ns + int64(delayMS)*1_000_000
		if t < 0 {
			t = 0
		}
		return fmt.Sprintf("<%s>%02d:%02d:%02d.%09d</", p[1],
			t/3_600_000_000_000, t/60_000_000_000%60, t/1_000_000_000%60, t%1_000_000_000)
	})
}

func shiftChapters(ctx context.Context, src, out string, delayMS int) error {
	ext, err := tools.CmdContext(ctx, tools.MkvExtr, src, "chapters", "-")
	if err != nil {
		return err
	}
	xml, err := ext.Output()
	if err != nil {
		return fmt.Errorf("mkvextract chapters: %w", err)
	}
	if !strings.Contains(string(xml), "<ChapterAtom>") {
		return nil
	}
	tmp, err := os.CreateTemp("", "redsync-chapters-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(shiftChapterXML(string(xml), delayMS)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	prop, err := tools.CmdContext(ctx, tools.MkvProp, out, "--chapters", tmp.Name())
	if err != nil {
		return err
	}
	msg, err := prop.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 { // mkvpropedit: warnings only
		ui.Warn("mkvpropedit: " + strings.TrimSpace(string(msg)))
		return nil
	}
	if err != nil {
		return fmt.Errorf("mkvpropedit chapters: %w: %s", err, strings.TrimSpace(string(msg)))
	}
	return nil
}
