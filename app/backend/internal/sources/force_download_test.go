package sources

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// A release named for another show, found by a season search, cannot be
// picked even with force, but Force download sends it to every episode of
// the season searched and says so.
func TestForceDownload_SendsToSeasonSearched(t *testing.T) {
	f, sr, rel := seasonSearchRel(t, "Silver.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	ctx := context.Background()
	if _, err := sr.Choose(ctx, rel.ID, 0, true, domain.System); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("forced pick: %v, want no Slot", err)
	}
	if !sr.CanForceDownload(ctx, rel.ID) {
		t.Fatal("CanForceDownload is false for a release found by a season search")
	}
	ch, err := sr.ForceDownload(ctx, rel.ID, domain.System)
	if err != nil || !ch.Forced {
		t.Fatalf("force download: %+v %v", ch, err)
	}
	if !strings.Contains(strings.Join(ch.Warnings, " "), "force downloaded") {
		t.Errorf("warnings %v do not say it was force downloaded", ch.Warnings)
	}
	for _, l := range []string{"S01E01", "S01E02", "S01E03"} {
		if st := f.state(t, l); st != mediafiles.SlotChosen {
			t.Errorf("%s is %s, want chosen", l, st)
		}
	}
	if st := f.state(t, "S02E01"); st == mediafiles.SlotChosen {
		t.Errorf("S02E01 was chosen from a season 1 search")
	}
}

// A release with no search behind it has nothing to be force downloaded to.
func TestForceDownload_NeedsASearch(t *testing.T) {
	f, sr, rel := seasonSearchRel(t, "Silver.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	exec(t, f.fixture, "UPDATE releases SET job_id = NULL WHERE id = ?", rel.ID)
	ctx := context.Background()
	if sr.CanForceDownload(ctx, rel.ID) {
		t.Error("CanForceDownload is true with no search")
	}
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); !errors.Is(err, ErrNoSlot) {
		t.Errorf("force download with no search: %v, want no Slot", err)
	}
}
