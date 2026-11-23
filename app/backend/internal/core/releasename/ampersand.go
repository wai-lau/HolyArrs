package releasename

import "slices"

// ampToAnd reads a lower-case "amp" between two words as "&": it is what an
// HTML "&amp;" leaves once the punctuation is gone. A first or last "amp",
// or one in capitals, stays a word.
func ampToAnd(toks []string) []string {
	out := slices.Clone(toks)
	for i := 1; i+1 < len(out); i++ {
		if out[i] == "amp" {
			out[i] = "&"
		}
	}
	return out
}
