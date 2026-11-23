package releasename

import (
	"slices"
	"strings"
)

// latinRun drops the CJK alt-titles of a title: the CJK words before the
// Latin title and the CJK run after it ("新吊带袜天使 New Panty Stocking",
// "New Panty Stocking パンティ ストッキングwithガーターベルト"). A CJK run may
// have Latin letters glued inside, so every word with a CJK character is part
// of it; an "&" left at the cut goes too. The Latin title is the words from
// the first word without CJK (a number may open it) to the next CJK word. A title
// with no such word (pure CJK, or only glued words) stays whole.
func latinRun(words []string) []string {
	if !slices.ContainsFunc(words, func(w string) bool { return strings.IndexFunc(w, isLatin) >= 0 && !isCJK(w) }) {
		return words
	}
	start := slices.IndexFunc(words, func(w string) bool { return !isCJK(w) })
	end := start
	for end < len(words) && !isCJK(words[end]) {
		end++
	}
	for end > start+1 && words[end-1] == "&" {
		end--
	}
	return words[start:end]
}
