package releasename

import "testing"

// A library name with "&" also shows in release names with the "&" left out
// ("Law Order"), as indexers drop it; the word "and" is not dropped from a
// name that never had an "&".
func TestSameNameAmpersandDropped(t *testing.T) {
	for _, c := range []struct {
		lib, rel string
		want     bool
	}{
		{"New PANTY & STOCKING with GARTERBELT", "New Panty Stocking with Garterbelt", true},
		{"New PANTY & STOCKING with GARTERBELT", "New PANTY & STOCKING with GARTERBELT", true},
		{"Law & Order", "Law and Order", true},
		{"Law & Order", "Law Order", true},
		{"Law and Order", "Law Order", false},
		{"Law & Order", "Law", false},
	} {
		if got := SameName(c.lib, CleanName(c.rel)); got != c.want {
			t.Errorf("SameName(%q, %q) = %v, want %v", c.lib, c.rel, got, c.want)
		}
	}
}
