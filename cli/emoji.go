package cli

import (
	"regexp"
	"sync"
)

var (
	emojiOnce sync.Once
	emojiRe   *regexp.Regexp
)

// replaceEmoji replaces every emoji sequence emoji-regex 8.0.0 matches with two spaces, as string-width 4.2.3
// does before counting columns.
func replaceEmoji(s string) string {
	emojiOnce.Do(func() { emojiRe = regexp.MustCompile(emojiPattern) })
	return emojiRe.ReplaceAllString(s, "  ")
}
