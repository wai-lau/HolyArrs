package mediafiles

import (
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// "&amp;" left as "amp" in a Release name, and a name that dropped the "&",
// still find a library name with an "&".
func TestNameShapesAmpersand(t *testing.T) {
	lib := []TitleRef{{ID: 1, Name: "New PANTY & STOCKING with GARTERBELT", Year: 2025, Kind: "show"}}
	for _, rel := range []string{
		"New Panty  amp  Stocking with Garterbelt S01E01 1080p WEB h264-GRP",
		"New Panty Stocking with Garterbelt S01E01 1080p WEB h264-GRP",
		"[DBD Raws] New PANTY  amp  STOCKING with GARTERBELT - 05 [1080p]",
	} {
		p := releasename.Parse(rel, releasename.Hints{Show: true})
		if m := MatchTitle(p, lib); m.Title == nil {
			t.Errorf("%q: no match; parsed title %q; %s", rel, p.Title.Value, m.Detail)
		}
	}
}
