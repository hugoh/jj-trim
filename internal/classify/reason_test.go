package classify_test

import (
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/stretchr/testify/assert"
)

// allReasons is every Reason constant classify declares. Kept in sync by
// hand: TestReasonRegistryComplete fails loudly if a Reason is added here
// without a matching classify.Describe entry, or vice versa isn't checked
// (an orphaned reasonInfo entry is harmless), so the common mistake — adding
// a new detection type and forgetting its metadata — is caught here.
//
//nolint:gochecknoglobals // test fixture, effectively constant
var allReasons = []classify.Reason{
	classify.ReasonMerged,
	classify.ReasonProbablyMerged,
	classify.ReasonStale,
	classify.ReasonNoDescription,
	classify.ReasonHasDescription,
	classify.ReasonGitCommitDuplicate,
}

func TestReasonRegistryComplete(t *testing.T) {
	t.Parallel()

	seenShort := make(map[string]classify.Reason, len(allReasons))

	for _, r := range allReasons {
		var info classify.ReasonInfo

		assert.NotPanics(t, func() {
			info = classify.Describe(r)
		}, "Describe(%q) panicked — missing reasonInfo entry", r)

		assert.NotZero(t, info.Confidence, "Reason %q has no Confidence set", r)
		assert.NotEmpty(t, info.Short, "Reason %q has no Short description", r)
		assert.NotEmpty(t, info.Long, "Reason %q has no Long description", r)

		if other, ok := seenShort[info.Short]; ok {
			t.Errorf("Reason %q and %q share the same Short description %q", r, other, info.Short)
		}

		seenShort[info.Short] = r
	}
}

func TestDescribe_UnregisteredReasonPanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		classify.Describe(classify.Reason("not-a-real-reason"))
	})
}

func TestConfidence_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "sure", classify.ConfidenceSure.String())
	assert.Equal(t, "review", classify.ConfidenceReview.String())
	assert.Equal(t, "guess", classify.ConfidenceGuess.String())
	assert.Equal(t, "unknown", classify.Confidence(99).String())
}

func TestConfidence_Letter(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "H", classify.ConfidenceSure.Letter())
	assert.Equal(t, "M", classify.ConfidenceReview.Letter())
	assert.Equal(t, "L", classify.ConfidenceGuess.Letter())
	assert.Equal(t, "?", classify.Confidence(99).Letter())
}
