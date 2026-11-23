package sources

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// matchedByID says a stored Release was matched by the id its Source gave.
func matchedByID(rel domain.Release) bool {
	reasons, _ := rel.Verdict["reasons"].([]any)
	for _, r := range reasons {
		if m, _ := r.(map[string]any); m["rule"] == sourceIDRule && m["effect"] == "pass" {
			return true
		}
	}
	return false
}

// heldShowMiss says a waiting show Release no longer names the Slot's show
// (its year is not the show's), unless it was matched by the Source's id.
func (s *Searcher) heldShowMiss(ctx context.Context, sl mediafiles.Slot, rel domain.Release) bool {
	if matchedByID(rel) {
		return false
	}
	db := s.Store.Read()
	titleID, err := mediafiles.ItemTitleID(ctx, db, sl.ItemID)
	if err != nil {
		return false
	}
	var ti mediafiles.Title
	if err := mediafiles.LoadTitle(ctx, db, titleID, &ti); err != nil || ti.Kind != "show" {
		return false
	}
	p := releasename.Parse(rel.Name, releasename.Hints{Size: rel.Size, Show: true})
	return mediafiles.MatchRelease(p, []mediafiles.TitleRef{showSet{ti: ti}.ref()}).Title == nil
}

// showSearchSlots returns the episodes a Release that matched none can
// still be forced onto, and why it needs force. It applies to a Release
// found by a season search of a show whose name is one of that show's but
// only its year kept it from matching, or whose name holds one of the
// show's names as whole words in a row among other words (a group or an
// alternate title the parser kept in the title). The Slots are those of the
// episodes its numbers cover, or every episode of the season searched when
// the name gives no numbers, in the season and Version searched; a Release
// naming another season gets none. skipped says which episodes were left
// out for a protected copy or a copy as good; wider is set when the Release
// holds the season searched and another one. Anything unclear returns none.
func (s *Searcher) showSearchSlots(ctx context.Context, rel domain.Release) (out []int64, skipped, why []string, wider bool) {
	if rel.JobID == nil {
		return nil, nil, nil, false
	}
	db := s.Store.Read()
	var job domain.Job
	if err := database.Get(ctx, db, "jobs", *rel.JobID, &job); err != nil || job.Kind != jobs.KindSearchSeason || job.Subject == nil || job.Subject.Type != "part" {
		return nil, nil, nil, false
	}
	vid, _ := job.Params["version_id"].(float64)
	var part mediafiles.Part
	var ti mediafiles.Title
	var ver mediafiles.Version
	if mediafiles.LoadPart(ctx, db, job.Subject.ID, &part) != nil || part.Number == nil || mediafiles.LoadTitle(ctx, db, part.TitleID, &ti) != nil ||
		mediafiles.LoadVersion(ctx, db, int64(vid), &ver) != nil || ver.TitleID != ti.ID {
		return nil, nil, nil, false
	}
	set, err := s.loadShow(ctx, ti, ver)
	if err != nil {
		return nil, nil, nil, false
	}
	p := releasename.Parse(rel.Name, releasename.Hints{Size: rel.Size, Show: true})
	lib := []mediafiles.TitleRef{set.ref()}
	if mediafiles.MatchRelease(p, lib).Title != nil {
		return nil, nil, nil, false
	}
	if mediafiles.MatchTitle(p, lib).Title != nil {
		why = append(why, "its year did not match "+ti.Name+"; it was found searching for it")
	} else if looseShowName(p.Title.Value, lib[0]) {
		why = append(why, "its name only loosely matched "+ti.Name+"; it was found searching for it")
	} else {
		return nil, nil, nil, false
	}
	refs := make([]mediafiles.EpisodeRef, len(set.eps))
	slots := map[int64]*mediafiles.Slot{}
	for i, e := range set.eps {
		refs[i] = e.ref
		slots[e.it.ID] = e.sl
	}
	var held []mediafiles.EpisodeRef
	if p.Show == nil {
		for _, e := range refs {
			if e.Season == *part.Number {
				held = append(held, e)
			}
		}
		why = append(why, fmt.Sprintf("its name gives no episode numbers; it goes to all of season %d", *part.Number))
	} else {
		var miss string
		if held, miss = mediafiles.MatchEpisodes(p, set.order(), refs); miss != "" {
			return nil, nil, nil, false
		}
	}
	jr := mediafiles.ReleaseFacts{Name: rel.Name, Parse: p, Size: rel.Size, Protocol: rel.Protocol}
	if slices.ContainsFunc(held, func(e mediafiles.EpisodeRef) bool { return e.Season != *part.Number }) {
		return nil, nil, nil, slices.ContainsFunc(held, func(e mediafiles.EpisodeRef) bool { return e.Season == *part.Number })
	}
	for _, e := range held {
		sl := slots[e.ItemID]
		if sl == nil || !canTakePick(sl.State) && !StuckChoice(*sl, time.Now()) || turnedDownFor(rel, sl.ID) {
			continue
		}
		if slotHasProtectedCopy(ctx, db, sl.ID) {
			skipped = append(skipped, e.Label()+" has a protected copy")
			continue
		}
		if c, err := s.loadSlot(ctx, sl.ID); err == nil && c.cur != nil && !mediafiles.Gains(jr, c.pr.Body, c.cur) {
			skipped = append(skipped, e.Label()+" already has a copy as good")
			continue
		}
		out = append(out, sl.ID)
	}
	return out, skipped, why, false
}

// looseShowName says whether one of the show's names shows in a release
// title as whole words in a row. "&", "and" and a stray "amp" (left by an
// escaped "&amp;") are left out on both sides, since release names drop
// or spell them in any of these ways.
func looseShowName(title string, t mediafiles.TitleRef) bool {
	have := looseWords(title)
	for _, n := range append([]string{t.Name}, t.Aliases...) {
		for _, form := range releasename.NameForms(n) {
			if want := looseWords(form); len(want) > 0 && containsRun(have, want) {
				return true
			}
		}
	}
	return false
}

// looseWords are the clean words of a name with "and" and "amp" left out.
func looseWords(name string) []string {
	return slices.DeleteFunc(strings.Fields(releasename.CleanName(name)), func(w string) bool { return w == "and" || w == "amp" })
}

// containsRun says whether want shows in have as words in a row.
func containsRun(have, want []string) bool {
	for i := 0; i+len(want) <= len(have); i++ {
		if slices.Equal(have[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
