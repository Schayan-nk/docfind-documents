package find

import (
	"fmt"
	"time"
)

// pct returns the p-th percentile of a sorted sample, nearest-rank.
func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	if i < 1 {
		i = 1
	}
	if i > len(sorted) {
		i = len(sorted)
	}
	return sorted[i-1]
}

var _ = fmt.Sprint // keep fmt available for ad-hoc logging while tuning
