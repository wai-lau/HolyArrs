package mediafiles

import (
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// Two torrents equal on everything before seeders, 60 and 190 seeders
// (once one seeder band): the one with more seeders wins, not the older one.
func TestRankMoreSeedersInSameBand(t *testing.T) {
	rules := GoodEveryday()
	mk := func(name string, seeders int, age time.Duration) (ReleaseFacts, Verdict) {
		p := releasename.Parse(name, releasename.Hints{})
		set := SlotSet{Match: MatchTitle(p, showLibrary), SlotIDs: []int64{1}, Out: true, RuntimeMinutes: 45}
		rel := ReleaseFacts{Name: name, Parse: p, Size: 1200 * mb, Protocol: "torrent", Seeders: &seeders, PublishedAt: time.Now().Add(-age)}
		return rel, Judge(rel, set, rules)
	}
	r1, v1 := mk("Copper.Valley.S01E01.1080p.WEB.H264-FLUX", 60, 48*time.Hour)
	r2, v2 := mk("Copper.Valley.S01E01.1080p.WEB.H264-FLUX", 190, time.Hour)
	vs := []Verdict{v1, v2}
	Rank([]ReleaseFacts{r1, r2}, vs, rules)
	if vs[1].Rank != 1 {
		t.Errorf("190 seeders should rank first\n%s\n%s", dump(vs[0]), dump(vs[1]))
	}
}

// A missing seeder count ranks below any known one.
func TestRankSeederCountUnknownLast(t *testing.T) {
	rules := GoodEveryday()
	mk := func(seeders *int) (ReleaseFacts, Verdict) {
		name := "Copper.Valley.S01E01.1080p.WEB.H264-FLUX"
		p := releasename.Parse(name, releasename.Hints{})
		set := SlotSet{Match: MatchTitle(p, showLibrary), SlotIDs: []int64{1}, Out: true, RuntimeMinutes: 45}
		rel := ReleaseFacts{Name: name, Parse: p, Size: 1200 * mb, Protocol: "torrent", Seeders: seeders, PublishedAt: time.Now().Add(-time.Hour)}
		return rel, Judge(rel, set, rules)
	}
	five := 5
	r1, v1 := mk(nil)
	r2, v2 := mk(&five)
	vs := []Verdict{v1, v2}
	Rank([]ReleaseFacts{r1, r2}, vs, rules)
	if vs[0].Passed && vs[1].Rank != 1 {
		t.Errorf("a known seeder count should rank above a missing one\n%s\n%s", dump(vs[0]), dump(vs[1]))
	}
}
