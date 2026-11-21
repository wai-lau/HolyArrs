package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
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

// An episode that already has a better copy is left out, and named.
func TestForceDownload_KeepsBetterCopy(t *testing.T) {
	f, sr, rel := seasonSearchRel(t, "Silver.Valley.S01.480p.HDTV.x264-QUILL", func(f showFix) {
		addFile(t, f, f.slots["S01E01"], "Copper.Valley.S01E01.2160p.BluRay.REMUX.HEVC-GRP")
		exec(t, f.fixture, "UPDATE slots SET state = 'better_wanted' WHERE id = ?", f.slots["S01E01"])
	})
	ch, err := sr.ForceDownload(context.Background(), rel.ID, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if st := f.state(t, "S01E01"); st == mediafiles.SlotChosen {
		t.Error("S01E01 with a better copy was taken")
	}
	for _, l := range []string{"S01E02", "S01E03"} {
		if st := f.state(t, l); st != mediafiles.SlotChosen {
			t.Errorf("%s is %s, want chosen", l, st)
		}
	}
	if !slices.ContainsFunc(ch.Warnings, func(w string) bool { return strings.Contains(w, "S01E01 already has a copy as good") }) {
		t.Errorf("the left-out episode is not named: %v", ch.Warnings)
	}
}

// A pack that also holds another season is not sent to the season searched.
func TestForceDownload_RefusesWiderPack(t *testing.T) {
	_, sr, rel := seasonSearchRel(t, "Silver.Valley.S01-S03.1080p.WEB-DL.H.264-QUILL", nil)
	ctx := context.Background()
	if sr.CanForceDownload(ctx, rel.ID) {
		t.Error("CanForceDownload is true for a pack of seasons 1 to 3")
	}
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); !errors.Is(err, ErrNoSlot) {
		t.Errorf("force download of a wider pack: %v, want no Slot", err)
	}
}

// An episode the release was turned down for is left out.
func TestForceDownload_SkipsTurnedDownSlot(t *testing.T) {
	f, sr, rel := seasonSearchRel(t, "Silver.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	ctx := context.Background()
	if err := sr.TurnDownFor(ctx, rel.ID, f.slots["S01E02"], "", domain.System); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t, "S01E02"); st == mediafiles.SlotChosen {
		t.Error("S01E02, turned down for this release, was taken")
	}
	if st := f.state(t, "S01E01"); st != mediafiles.SlotChosen {
		t.Errorf("S01E01 is %s, want chosen", st)
	}
}

// A release that matched a Slot by name is picked the normal way, never force downloaded.
func TestForceDownload_RefusesMatchedRelease(t *testing.T) {
	f, sr, rel := seasonSearchRel(t, "Copper.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	if len(rel.MatchedSlotIDs) == 0 {
		t.Fatal("the release matched no Slot")
	}
	ctx := context.Background()
	if sr.CanForceDownload(ctx, rel.ID) {
		t.Error("CanForceDownload is true for a matched release")
	}
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); !errors.Is(err, ErrNoSlot) {
		t.Errorf("force download of a matched release: %v, want no Slot", err)
	}
	if st := f.state(t, "S01E01"); st == mediafiles.SlotChosen {
		t.Error("S01E01 was chosen")
	}
}

// A second click finds the Slots already taken and says they are busy.
func TestForceDownload_SecondClickIsBusy(t *testing.T) {
	_, sr, rel := seasonSearchRel(t, "Silver.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	ctx := context.Background()
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.ForceDownload(ctx, rel.ID, domain.System); !errors.Is(err, ErrBusy) {
		t.Errorf("second force download: %v, want busy", err)
	}
}

// Over HTTP: a forced pick that matches nothing answers 409 needs_force_download,
// force_download then sends it, and a release that matched is not force downloaded.
func TestChooseRoute_ForceDownload(t *testing.T) {
	f, _, rel := seasonSearchRel(t, "Silver.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	srv := routesServer(t, f.s, nil)
	url := fmt.Sprintf("%s/api/v1/releases/%d/choose", srv.URL, rel.ID)
	if b := do(t, "POST", url, `{"force": true}`, 409); !strings.Contains(string(b), `"needs_force_download":true`) {
		t.Errorf("409 body lacks needs_force_download: %s", b)
	}
	do(t, "POST", url, `{"force_download": true}`, 200)
	do(t, "POST", url, `{"force_download": true}`, 409)
	if st := f.state(t, "S01E01"); st != mediafiles.SlotChosen {
		t.Errorf("S01E01 is %s, want chosen", st)
	}
}

func TestChooseRoute_ForceDownloadGate(t *testing.T) {
	f, _, rel := seasonSearchRel(t, "Copper.Valley.S01.1080p.WEB-DL.H.264-QUILL", nil)
	srv := routesServer(t, f.s, nil)
	url := fmt.Sprintf("%s/api/v1/releases/%d/choose", srv.URL, rel.ID)
	do(t, "POST", url, `{"force_download": true}`, 400)
	if st := f.state(t, "S01E01"); st == mediafiles.SlotChosen {
		t.Error("a matched release was force downloaded")
	}
}
