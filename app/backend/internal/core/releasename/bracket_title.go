package releasename

import (
	"regexp"
	"slices"
	"strings"
)

// reBracketEpisodes reads the episode bracket of [Group][Title][01-12][tags]:
// a first number, then an optional last one after a hyphen, a tilde or
// spaces (the indexer turns the hyphen into a space), then anything.
var reBracketEpisodes = regexp.MustCompile(`^(\d{1,4})(?:v(\d))?(?:$|[\s~-])\s*[~-]?\s*(\d{1,4})?`)

// bracketTitleShow reads the layout some fansub groups use: the title in a
// bracket of its own after the group, then a bracket with the episode
// numbers, as in "[Group][Title][01-12][1080p][BDRip]". body starts after
// the group. It is a running number or range, like "Title - 01-12", so the
// numbers are absolute. The title bracket is returned as the title text;
// the rest of the name is the tags. A bracket after the title that does not
// start with an episode number (a resolution, a year, a codec) is no match.
func bracketTitleShow(body string, anime bool) (h showHit, title, post string, ok bool) {
	if !anime {
		return h, "", "", false
	}
	t := reLeadBrack.FindStringSubmatchIndex(body)
	if t == nil {
		return h, "", "", false
	}
	rest := body[t[1]:]
	e := reLeadBrack.FindStringSubmatchIndex(rest)
	if e == nil {
		return h, "", "", false
	}
	m := reBracketEpisodes.FindStringSubmatch(rest[e[2]:e[3]])
	if m == nil || isYearNum(m[1]) || reResWord.MatchString(m[1]+"p") {
		return h, "", "", false
	}
	titleText := body[t[2]:t[3]]
	if slices.ContainsFunc(strings.Fields(titleText), isTag) {
		return h, "", "", false
	}
	first := atoi(m[1])
	h = showHit{kind: KindAbsolute, n: ShowNumbers{Absolute: []int{first}}}
	if m[2] != "" {
		h.fix = atoi(m[2]) - 1
	}
	// The last number is a range end only when it is not a resolution
	// ("01 1080P") and does not run backwards.
	if m[3] != "" {
		inner := rest[e[2]:e[3]]
		after := inner[strings.Index(inner, m[3])+len(m[3]):]
		if last := atoi(m[3]); last > first && !(after != "" && strings.ContainsAny(after[:1], "pPiI")) {
			if sp := span(first, last); sp != nil {
				h.n.Absolute = sp
			}
		}
	}
	return h, latinTitle(titleText), rest[e[1]:], true
}

// latinTitle is a bracket title without its CJK alt-titles: "新吊带袜天使 New
// PANTY STOCKING" is the Latin title; a title with no Latin words stays whole.
func latinTitle(s string) string {
	words := strings.Fields(s)
	hasLatin := slices.ContainsFunc(words, func(w string) bool { return strings.IndexFunc(w, isLatin) >= 0 })
	if !hasLatin {
		return s
	}
	return strings.Join(slices.DeleteFunc(words, func(w string) bool { return isCJK(w) && strings.IndexFunc(w, isLatin) < 0 }), " ")
}
