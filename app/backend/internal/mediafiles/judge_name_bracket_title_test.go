package mediafiles

import (
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// A fansub name with the title in its own bracket, and a CJK alt-title in
// front of the Latin one, still finds the show.
func TestNameShapesBracketTitle(t *testing.T) {
	lib := []TitleRef{{ID: 1, Name: "New PANTY STOCKING with GARTERBELT", Year: 2025, Kind: "show", OriginalLanguage: "ja"}}
	for _, rel := range []string{
		"[DBD Raws][新吊带袜天使 New PANTY STOCKING with GARTERBELT][01 13TV全集 特典映像][1080P][BDRip][HEVC 10bit][简繁日双语外挂][FLAC AC3][MKV](新しいパンティ＆ストッキングwithガーターベルト)",
		"[DBD Raws][New PANTY STOCKING with GARTERBELT][01-13][1080P][BDRip]",
		"[DBD Raws] New PANTY STOCKING with GARTERBELT - 05 [1080p]",
	} {
		p := releasename.Parse(rel, releasename.Hints{Show: true})
		if m := MatchTitle(p, lib); m.Title == nil {
			t.Errorf("%q: no match; parsed title %q; %s", rel, p.Title.Value, m.Detail)
		}
	}
}
