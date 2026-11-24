package sources

import (
	"context"
	"slices"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// forceDownloadWhy is the line a forced download carries in its warnings.
const forceDownloadWhy = "it was force downloaded and may not be what was searched for"

// ForceDownload sends a Release that matched no Slot to every Slot of the
// season or album its search looked for, whatever its name says, in the
// Version searched. It is the last step after "Pick anyway" found no Slot
// for the Release. Slots that cannot take a pick, Slots the Release was
// turned down for, and Slots with a protected or better copy are left out;
// so is a pack that also holds another season. A Release that matched a
// Slot by name is picked the normal way, so it returns ErrNoSlot, as it does
// with no search behind it or no Slot left. When every Slot was taken
// already, it says they are busy.
func (s *Searcher) ForceDownload(ctx context.Context, releaseID int64, by domain.Actor) (Choice, error) {
	var rel domain.Release
	if err := database.Get(ctx, s.Store.Read(), "releases", releaseID, &rel); err != nil {
		return Choice{}, ErrNoRelease
	}
	ids, skipped, album, busy := s.searchedSlots(ctx, rel)
	if len(ids) == 0 {
		if busy != "" {
			return Choice{}, busyErr(busy)
		}
		return Choice{}, ErrNoSlot
	}
	return s.chooseMany(ctx, rel, ids, skipped, album, true, []string{forceDownloadWhy}, by)
}

// CanForceDownload says whether ForceDownload has a Slot to send the Release to.
func (s *Searcher) CanForceDownload(ctx context.Context, releaseID int64) bool {
	var rel domain.Release
	if err := database.Get(ctx, s.Store.Read(), "releases", releaseID, &rel); err != nil {
		return false
	}
	ids, _, _, _ := s.searchedSlots(ctx, rel)
	return len(ids) > 0
}

// searchedSlots returns the Slots the season or album search that found rel
// looked for and that can take a pick, which were left out and why, and
// whether they are an album's tracks. When none can take a pick because they
// were taken already, busy is the state of one of them. A Release that
// matched a Slot, or a pack that also holds another season, gets none.
func (s *Searcher) searchedSlots(ctx context.Context, rel domain.Release) (out []int64, skipped []string, album bool, busy mediafiles.SlotState) {
	// A sent Release lists its Slots as matched, so only an unsent one is gated.
	if rel.JobID == nil || len(rel.MatchedSlotIDs) > 0 && rel.State != "chosen" {
		return nil, nil, false, ""
	}
	db := s.Store.Read()
	var job domain.Job
	if err := database.Get(ctx, db, "jobs", *rel.JobID, &job); err != nil || job.Subject == nil || job.Subject.Type != "part" ||
		job.Kind != jobs.KindSearchSeason && job.Kind != jobs.KindSearchAlbum {
		return nil, nil, false, ""
	}
	var part mediafiles.Part
	if mediafiles.LoadPart(ctx, db, job.Subject.ID, &part) != nil {
		return nil, nil, false, ""
	}
	album = job.Kind == jobs.KindSearchAlbum
	p := releasename.Parse(rel.Name, releasename.Hints{Size: rel.Size, Show: !album, Music: album})
	if !album && p.Show != nil && part.Number != nil && slices.ContainsFunc(p.Show.Seasons, func(n int) bool { return n != *part.Number }) {
		return nil, nil, false, ""
	}
	vid, _ := job.Params["version_id"].(float64)
	slots, err := mediafiles.VersionSlotsOfPart(ctx, db, int64(vid), part.ID)
	if err != nil {
		return nil, nil, false, ""
	}
	jr := mediafiles.ReleaseFacts{Name: rel.Name, Parse: p, Size: rel.Size, Protocol: rel.Protocol}
	for _, sl := range slots {
		if !canTakePick(sl.State) && !StuckChoice(sl, time.Now()) {
			busy = sl.State
			continue
		}
		if turnedDownFor(rel, sl.ID) {
			continue
		}
		what := "the copy"
		var it mediafiles.Item
		if mediafiles.LoadItem(ctx, db, sl.ItemID, &it) == nil && it.Kind == "episode" {
			what = episodeLabel(it)
		}
		if slotHasProtectedCopy(ctx, db, sl.ID) {
			skipped = append(skipped, what+" has a protected copy")
			continue
		}
		if c, err := s.loadSlot(ctx, sl.ID); err != nil || c.cur != nil && !mediafiles.Gains(jr, c.pr.Body, c.cur) {
			skipped = append(skipped, what+" already has a copy as good")
			continue
		}
		out = append(out, sl.ID)
	}
	return out, skipped, album, busy
}
