package sync

import (
	"slices"
	"testing"

	"github.com/720pixel/RedSync/internal/media"
	"github.com/720pixel/RedSync/internal/tools"
)

func TestChapterSyncOnlyWhenSourceHasChapters(t *testing.T) {
	job := func(has bool) Job {
		return Job{Output: "out.mkv", Contribs: []Contribution{
			{File: media.File{Path: "video.mkv"}, Video: []media.Track{{Index: 0, Kind: media.Video}}},
			{File: media.File{Path: "extra.mkv", HasChapters: has}, Chapters: true, Drift: Drift{DelayMS: 160}},
		}}
	}
	if slices.Contains(build(job(false)), "--chapter-sync") {
		t.Fatal("--chapter-sync passed for a source without chapters")
	}
	if got := slices.Contains(build(job(true)), "--chapter-sync"); got != !tools.LocalAvmux {
		t.Fatalf("--chapter-sync present = %v for a source with chapters, want %v", got, !tools.LocalAvmux)
	}
}
