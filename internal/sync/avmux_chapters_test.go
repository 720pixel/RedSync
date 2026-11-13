package sync

import "testing"

func TestShiftChapterXML(t *testing.T) {
	in := "<ChapterTimeStart>00:00:00.000000000</ChapterTimeStart><ChapterTimeEnd>00:59:59.900000000</ChapterTimeEnd>" +
		"<ChapterTimeStart>02:49:26.958000000</ChapterTimeStart>"
	got := shiftChapterXML(in, 160)
	want := "<ChapterTimeStart>00:00:00.160000000</ChapterTimeStart><ChapterTimeEnd>01:00:00.060000000</ChapterTimeEnd>" +
		"<ChapterTimeStart>02:49:27.118000000</ChapterTimeStart>"
	if got != want {
		t.Fatalf("+160ms:\n got %s\nwant %s", got, want)
	}
	if got := shiftChapterXML("<ChapterTimeStart>00:00:00.100000000</ChapterTimeStart>", -500); got != "<ChapterTimeStart>00:00:00.000000000</ChapterTimeStart>" {
		t.Fatalf("negative shift must clamp at zero, got %s", got)
	}
}
