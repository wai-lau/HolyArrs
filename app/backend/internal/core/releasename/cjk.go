package releasename

import (
	"strings"
	"unicode"
)

func isLatin(c rune) bool { return unicode.Is(unicode.Latin, c) && unicode.IsLetter(c) }

// isCJK says a word holds Han, kana or Hangul characters.
func isCJK(w string) bool {
	return strings.IndexFunc(w, func(c rune) bool {
		return unicode.In(c, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
	}) >= 0
}
