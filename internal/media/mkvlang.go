package media

import (
	"context"
	"encoding/json"

	"github.com/720pixel/RedSync/internal/tools"
)

// ffprobe only ever surfaces a track's legacy three-letter language code, even
// when the file carries a proper BCP-47 tag - so something like French always
// comes back as "fre" whether it's fr-FR or fr-CA. mkvmerge's identify output
// reads Matroska's language_ietf element instead, which is where a region or
// script actually lives. enrichLangIETF pulls just that field, per track ID,
// so Probe can upgrade the language it already has instead of guessing.
type mkvIdentify struct {
	Tracks []struct {
		ID         int `json:"id"`
		Properties struct {
			LanguageIETF string `json:"language_ietf"`
		} `json:"properties"`
	} `json:"tracks"`
}

// enrichLangIETF returns track ID -> BCP-47 language tag, or nil if mkvmerge
// isn't available or the file isn't one it can identify. mkvmerge's track IDs
// number video/audio/subtitle tracks in file order starting at 0, the same
// scheme ffprobe uses for stream index, so the two line up directly.
func enrichLangIETF(_ context.Context, path string) map[int]string {
	cmd, err := tools.Cmd(tools.MkvMerge, "-J", path)
	if err != nil {
		return nil
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var id mkvIdentify
	if json.Unmarshal(out, &id) != nil {
		return nil
	}
	langs := map[int]string{}
	for _, t := range id.Tracks {
		if l := t.Properties.LanguageIETF; l != "" && l != "und" {
			langs[t.ID] = l
		}
	}
	return langs
}

// applyLangIETF upgrades every track's Language to its BCP-47 form where
// mkvmerge found one.
func applyLangIETF(f *File, langs map[int]string) {
	upgrade := func(ts []Track) {
		for i := range ts {
			if l, ok := langs[ts[i].Index]; ok {
				ts[i].Language = l
			}
		}
	}
	upgrade(f.Video)
	upgrade(f.Audio)
	upgrade(f.Subs)
}
