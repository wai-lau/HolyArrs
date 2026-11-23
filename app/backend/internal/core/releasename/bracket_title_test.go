package releasename

import "testing"

// A bracket that holds only a resolution, a year or a codec is a tag, not
// the episode numbers of a [Group][Title][01] name, so such names stay as
// they were read before.
func TestBracketTitleNeedsEpisodeBracket(t *testing.T) {
	for _, n := range []string{
		"[Kumomaru][Starfall Kitchen][1080P][WEB-DL]",
		"[Kumomaru][Starfall Kitchen][2019][1080P]",
		"[Kumomaru][Starfall Kitchen][1080 x264][WEB-DL]",
	} {
		if p := Parse(n, Hints{Show: true}); p.Show != nil && len(p.Show.Absolute) > 0 && p.Show.Absolute[0] != 0 && p.Title.Value == "Starfall Kitchen" {
			t.Errorf("%s: read as episodes %+v", n, p.Show)
		}
	}
}
