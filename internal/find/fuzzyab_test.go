package find

import (
	"os"
	"sort"
	"testing"
	"time"
	"unicode/utf8"
)

// Does grouping the vocabulary by length actually make typo correction faster?
//
// Measured this way because measuring it the obvious way did not work. Running
// the latency benchmark before and after the change showed every query slower
// afterwards -- including the ones that do not touch this code at all -- which
// is machine noise, not a result. Two runs minutes apart on a busy laptop are
// not comparable, and reporting the difference between them would have been
// reporting the noise.
//
// So both implementations run here in one process, interleaved, against the
// same index: whatever the machine is doing, it is doing it to both.
func TestFuzzyLengthBucketingIsWorthIt(t *testing.T) {
	if os.Getenv("DOCFIND_SEARCHBENCH") == "" {
		t.Skip("set DOCFIND_SEARCHBENCH=1 to run the fuzzy A/B measurement")
	}

	n := 20000
	root := buildScaleArchive(t, n)
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// How much of the vocabulary the length filter can actually exclude for a
	// given query length decides whether this helps at all, so it is reported
	// rather than assumed.
	const probe = "valv3" // 5 runes, one edit from "valve"
	qn := utf8.RuneCountInString(probe)
	inBand := 0
	for ln := qn - 1; ln <= qn+1; ln++ {
		inBand += len(ix.vocabByLen[ln])
	}
	t.Logf("vocabulary %d terms; %d (%.1f%%) are within one character of a %d-character query",
		len(ix.vocab), inBand, 100*float64(inBand)/float64(len(ix.vocab)), qn)

	// The implementation as it was: walk the whole vocabulary, checking length
	// inside the loop.
	full := func(term string) string {
		n := utf8.RuneCountInString(term)
		if n < 3 || n > 24 {
			return ""
		}
		maxDist := 1
		if n >= 7 {
			maxDist = 2
		}
		ra := []rune(term)
		var buf levBuf
		best, bestDist := "", maxDist+1
		for _, vocab := range ix.vocab {
			if vn := utf8.RuneCountInString(vocab); vn < n-maxDist || vn > n+maxDist {
				continue
			}
			if d, ok := buf.within(ra, vocab, bestDist-1); ok {
				best, bestDist = vocab, d
			}
		}
		return best
	}

	// Same answer from both, or the speed comparison is meaningless.
	for _, typo := range []string{"valv3", "flang3", "suppl1er", "gask3t", "b3aring"} {
		bucketed, _ := ix.fuzzyPostings(typo)
		if ref := full(typo); bucketed != ref {
			t.Fatalf("the two implementations disagree on %q: bucketed %q, full scan %q",
				typo, bucketed, ref)
		}
	}

	const runs = 30
	var aSamples, bSamples []time.Duration
	// Interleaved, and each warmed once, so neither pays for a cold start and
	// a drift in machine load lands on both.
	ix.fuzzyPostings(probe)
	full(probe)
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		ix.fuzzyPostings(probe)
		aSamples = append(aSamples, time.Since(t0))

		t1 := time.Now()
		full(probe)
		bSamples = append(bSamples, time.Since(t1))
	}
	sort.Slice(aSamples, func(i, j int) bool { return aSamples[i] < aSamples[j] })
	sort.Slice(bSamples, func(i, j int) bool { return bSamples[i] < bSamples[j] })

	aMed, bMed := pct(aSamples, 50), pct(bSamples, 50)
	t.Logf("typo correction, %d interleaved runs each:", runs)
	t.Logf("  length-bucketed : p50 %s  p95 %s", aMed.Round(time.Microsecond), pct(aSamples, 95).Round(time.Microsecond))
	t.Logf("  full vocabulary : p50 %s  p95 %s", bMed.Round(time.Microsecond), pct(bSamples, 95).Round(time.Microsecond))
	if aMed > 0 {
		t.Logf("  ratio           : %.2fx", float64(bMed)/float64(aMed))
	}

	// Not asserted as a speedup target: on a corpus whose vocabulary is mostly
	// short numeric tokens the length band excludes little, and this is allowed
	// to be a wash. What must not happen is it being SLOWER -- that would mean
	// the extra map lookup per group cost more than the scan it replaced.
	if aMed > bMed*3/2 {
		t.Errorf("length bucketing is materially slower than the full walk "+
			"(%s vs %s); the grouping is costing more than it saves", aMed, bMed)
	}
}
