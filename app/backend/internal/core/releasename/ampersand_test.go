package releasename

import "testing"

// A stray "amp" between words is what is left of an HTML "&amp;".
func TestAmpWordReadsAsAmpersand(t *testing.T) {
	for in, want := range map[string]string{
		"Law amp Order S01E01 1080p WEB h264-GRP": "Law & Order",
		"Amp S01E01 1080p WEB h264-GRP":           "Amp",
		"Tesla amp S01E01 1080p WEB h264-GRP":     "Tesla amp",
	} {
		if p := Parse(in, Hints{}); p.Title.Value != want {
			t.Errorf("%s: title %q, want %q", in, p.Title.Value, want)
		}
	}
	if p := Parse("Law amp Order 1080p WEB h264-GRP", Hints{}); p.Title.Value != "Law & Order" {
		t.Errorf("movie title %q, want %q", p.Title.Value, "Law & Order")
	}
}
