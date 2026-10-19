package auth

import (
	"slices"
	"strings"
)

// The notification topics a person has switched off.
//
// They live in Prefs so they travel with the account -- PostgreSQL's
// user_prefs row and the emergency snapshot -- with no schema change. The
// store knows nothing of what a topic means; it only keeps the list tidy.
// Which names are real is the sender's business, and the sender checks.

// maxMuted is more topics than exist; it bounds what a hand-made request can
// make the store keep.
const maxMuted = 32

func quietHour(h int) bool { return h >= 0 && h <= 23 }

// cleanTopics lower-cases, de-duplicates and sorts, and drops anything that
// is not a plain topic name. Empty comes back nil, so an account with nothing
// switched off stores no "muted" key at all.
func cleanTopics(in []string) []string {
	var out []string
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if !topicName(t) || slices.Contains(out, t) {
			continue
		}
		out = append(out, t)
		if len(out) == maxMuted {
			break
		}
	}
	slices.Sort(out)
	return out
}

// topicName is a-z, 0-9 and hyphens, 1 to 32 of them.
func topicName(t string) bool {
	if t == "" || len(t) > 32 {
		return false
	}
	for _, r := range t {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
