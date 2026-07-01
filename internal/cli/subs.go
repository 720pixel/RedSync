package cli

import (
	"strings"

	"github.com/720pixel/RedSync/internal/media"
)

// langAlias folds legacy three-letter language codes onto their two-letter
// form, so tags that spell the same language differently (e.g. "fre" and
// "fr") still count as one language for --unique. Only the base subtag is
// touched - anything after a hyphen (a region or script, like "-CA" or
// "-Hant") is left untouched, since that's exactly what makes something like
// fr-CA distinct from fr-FR.
var langAlias = map[string]string{
	"eng": "en", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de",
	"spa": "es", "ita": "it", "por": "pt", "dut": "nl", "nld": "nl",
	"swe": "sv", "dan": "da", "nor": "no", "nob": "no", "nno": "no",
	"fin": "fi", "pol": "pl", "rus": "ru", "ukr": "uk", "cze": "cs", "ces": "cs",
	"slo": "sk", "slk": "sk", "hun": "hu", "rum": "ro", "ron": "ro",
	"bul": "bg", "gre": "el", "ell": "el", "tur": "tr", "ara": "ar",
	"heb": "he", "jpn": "ja", "kor": "ko", "chi": "zh", "zho": "zh",
	"vie": "vi", "tha": "th", "ind": "id", "may": "ms", "msa": "ms",
	"hin": "hi", "ben": "bn", "tam": "ta", "tel": "te", "mar": "mr",
	"urd": "ur", "per": "fa", "fas": "fa", "cat": "ca", "baq": "eu", "eus": "eu",
	"glg": "gl", "gle": "ga", "wel": "cy", "cym": "cy", "ice": "is", "isl": "is",
	"lav": "lv", "lit": "lt", "est": "et", "slv": "sl", "hrv": "hr", "srp": "sr",
	"mac": "mk", "mkd": "mk", "alb": "sq", "sqi": "sq", "arm": "hy", "hye": "hy",
	"geo": "ka", "kat": "ka", "aze": "az", "kaz": "kk", "uzb": "uz",
}

// normalizeLangTag folds a track's language tag into a comparable key: the
// base language is canonicalised through langAlias and lowercased, with any
// region/script subtag kept exactly as given.
func normalizeLangTag(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" || lang == "und" || lang == "none" {
		return "und"
	}
	base, rest := lang, ""
	if i := strings.IndexByte(lang, '-'); i >= 0 {
		base, rest = lang[:i], lang[i:]
	}
	if alias, ok := langAlias[base]; ok {
		base = alias
	}
	return base + rest
}

// subRoleKey is what makes two subtitle tracks "the same" under --unique:
// same language, same forced/SDH flags, and - when either carries a custom
// title - the same title. The title check means a "Signs" or other custom
// labelled track never gets silently dropped just for sharing a language
// with a plain subtitle track.
func subRoleKey(t media.Track) string {
	title := strings.ToLower(strings.TrimSpace(t.Title))
	return normalizeLangTag(t.Language) + "|" + boolTag(t.Forced) + "|" + boolTag(t.HearImp) + "|" + title
}

func boolTag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
