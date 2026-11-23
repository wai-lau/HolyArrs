package sources

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// A Release found by a season search whose name holds the show's name among
// other words matched no Title, but "Pick anyway" still sends it: to the
// episodes its numbers name, or to the whole season searched when it names
// none.
func TestChoose_LooseNameFromSeasonSearch(t *testing.T) {
	for _, c := range []struct {
		name string
		want []string
	}{
		{"Watch Copper Valley QUILL [WEB DL 1080p]", []string{"S01E01", "S01E02", "S01E03"}},
		{"Watch Copper Valley S01 1080p WEB-DL H.264-QUILL", []string{"S01E01", "S01E02", "S01E03"}},
	} {
		f, sr, rel := seasonSearchRel(t, c.name, nil)
		if len(rel.MatchedSlotIDs) != 0 {
			t.Fatalf("%s matched %v; the case needs a name that matches none", c.name, rel.MatchedSlotIDs)
		}
		ctx := context.Background()
		var nf *NeedsForceError
		if _, err := sr.Choose(ctx, rel.ID, 0, false, domain.System); !errors.As(err, &nf) || !strings.Contains(strings.Join(nf.Reasons, " "), "loosely") {
			t.Fatalf("%s without force: %v, want needs force saying the name only loosely matched", c.name, err)
		}
		ch, err := sr.Choose(ctx, rel.ID, 0, true, domain.System)
		if err != nil || !ch.Forced {
			t.Fatalf("%s forced: %+v %v", c.name, ch, err)
		}
		for _, l := range c.want {
			if st := f.state(t, l); st != mediafiles.SlotChosen {
				t.Errorf("%s: %s is %s, want chosen", c.name, l, st)
			}
		}
		if st := f.state(t, "S02E01"); st == mediafiles.SlotChosen {
			t.Errorf("%s: S02E01 was chosen from a season 1 search", c.name)
		}
	}
}

// The show's name must show as whole words in a row: a longer word, or
// another season's numbers, still refuse.
func TestChoose_LooseNameNeedsTheWholeName(t *testing.T) {
	for _, name := range []string{
		"Watch Copper Valleyview QUILL [WEB DL 1080p]",
		"Watch Copper Big Valley QUILL [WEB DL 1080p]",
		"Watch Copper Valley S02 1080p WEB-DL H.264-QUILL",
	} {
		_, sr, rel := seasonSearchRel(t, name, nil)
		if len(rel.MatchedSlotIDs) != 0 {
			t.Fatalf("%s matched %v, want none", name, rel.MatchedSlotIDs)
		}
		if _, err := sr.Choose(context.Background(), rel.ID, 0, true, domain.System); !errors.Is(err, ErrNoSlot) {
			t.Errorf("%s was forced from the season search: %v", name, err)
		}
	}
}
