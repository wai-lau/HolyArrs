package mediafiles

import (
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// A CJK alt-title next to the Latin title, before or after it, is not part
// of the Title that is matched.
func TestNameShapesCJKAltTitle(t *testing.T) {
	lib := []TitleRef{{ID: 1, Name: "New PANTY & STOCKING with GARTERBELT", Year: 2025, Kind: "show", OriginalLanguage: "ja"}}
	for _, rel := range []string{
		"[7³ACG] New Panty & Stocking with Garterbelt パンティ & ストッキングwithガーターベルト S2 [BDRip 1080p AV1 OPUS]",
		"[Kumomaru] 新吊带袜天使 New Panty & Stocking with Garterbelt S2 [1080p]",
	} {
		p := releasename.Parse(rel, releasename.Hints{Show: true})
		if m := MatchTitle(p, lib); m.Title == nil {
			t.Errorf("%q: no match; parsed title %q; %s", rel, p.Title.Value, m.Detail)
		}
	}
}
