package sources

import (
	"context"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// forceDownloadWhy is the line a forced download carries in its warnings.
const forceDownloadWhy = "it was force downloaded and may not be what was searched for"

// ForceDownload sends a Release to whatever the search that found it looked
// for, whatever its name says: the movie Slot of a search_slot Job, or every
// Slot of the season or album of a search_season or search_album Job, in the
// Version searched. It is the last step after "Pick anyway" found no Slot
// for the Release. Slots that cannot take a pick and Slots with a protected
// copy are left out; nothing else holds it back. With no search behind it,
// or no Slot left, it returns ErrNoSlot.
func (s *Searcher) ForceDownload(ctx context.Context, releaseID int64, by domain.Actor) (Choice, error) {
	var rel domain.Release
	if err := database.Get(ctx, s.Store.Read(), "releases", releaseID, &rel); err != nil {
		return Choice{}, ErrNoRelease
	}
	ids, skipped, album := s.searchedSlots(ctx, rel)
	if len(ids) == 0 {
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
	ids, _, _ := s.searchedSlots(ctx, rel)
	return len(ids) > 0
}

// searchedSlots returns the Slots the search that found rel looked for that
// can take a pick, which were left out for a protected copy, and whether
// they are an album's tracks.
func (s *Searcher) searchedSlots(ctx context.Context, rel domain.Release) (out []int64, skipped []string, album bool) {
	if rel.JobID == nil {
		return nil, nil, false
	}
	db := s.Store.Read()
	var job domain.Job
	if err := database.Get(ctx, db, "jobs", *rel.JobID, &job); err != nil || job.Subject == nil {
		return nil, nil, false
	}
	var slots []mediafiles.Slot
	switch {
	case job.Kind == jobs.KindSearchSlot && job.Subject.Type == "slot":
		c, err := s.loadSlot(ctx, job.Subject.ID)
		if err != nil {
			return nil, nil, false
		}
		slots = []mediafiles.Slot{c.sl}
	case (job.Kind == jobs.KindSearchSeason || job.Kind == jobs.KindSearchAlbum) && job.Subject.Type == "part":
		vid, _ := job.Params["version_id"].(float64)
		var err error
		if slots, err = mediafiles.VersionSlotsOfPart(ctx, db, int64(vid), job.Subject.ID); err != nil {
			return nil, nil, false
		}
		album = job.Kind == jobs.KindSearchAlbum
	default:
		return nil, nil, false
	}
	for _, sl := range slots {
		if !canTakePick(sl.State) && !StuckChoice(sl, time.Now()) {
			continue
		}
		if slotHasProtectedCopy(ctx, db, sl.ID) {
			what := "the copy"
			var it mediafiles.Item
			if mediafiles.LoadItem(ctx, db, sl.ItemID, &it) == nil && it.Kind == "episode" {
				what = episodeLabel(it)
			}
			skipped = append(skipped, what+" has a protected copy")
			continue
		}
		out = append(out, sl.ID)
	}
	return out, skipped, album
}
