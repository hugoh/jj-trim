package classify_test

import (
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/stretchr/testify/assert"
)

const (
	parent1  = "parent1"
	parentA  = "parentA"
	parentB  = "parentB"
	orphanID = "orphan"
	forkID   = "fork"
)

func candidate(t *testing.T, id string, parents []string, diffHash string) classify.Candidate {
	t.Helper()

	return classify.Candidate{
		ChangeID:        id,
		ParentChangeIDs: parents,
		DiffHash:        diffHash,
	}
}

type duplicateTest struct {
	name     string
	fork     classify.Candidate
	kept     []classify.Candidate
	wantDups []string
	wantRest []string
}

func TestGitCommitDuplicates(t *testing.T) {
	t.Parallel()

	tests := []duplicateTest{
		{
			name:     "matching_parent_and_hash_is_duplicate",
			fork:     candidate(t, orphanID, []string{parent1}, "hash-a"),
			kept:     []classify.Candidate{candidate(t, "kept", []string{parent1}, "hash-a")},
			wantDups: []string{orphanID},
			wantRest: []string{},
		},
		{
			name:     "same_parent_different_hash_not_duplicate",
			fork:     candidate(t, forkID, []string{parent1}, "hash-a"),
			kept:     []classify.Candidate{candidate(t, "kept", []string{parent1}, "hash-b")},
			wantDups: []string{},
			wantRest: []string{forkID},
		},
		{
			name:     "same_hash_different_parent_not_duplicate",
			fork:     candidate(t, forkID, []string{parentA}, "hash-a"),
			kept:     []classify.Candidate{candidate(t, "kept", []string{parentB}, "hash-a")},
			wantDups: []string{},
			wantRest: []string{forkID},
		},
		{
			// DuplicateKey's sort: a merge commit's parent order isn't guaranteed to
			// match between two independently queried candidates, so comparing parent
			// sets must not be sensitive to order.
			name: "merge_commit_parents_order_independent",
			fork: candidate(t, orphanID, []string{parentB, parentA}, "hash-a"),
			kept: []classify.Candidate{
				candidate(t, "kept", []string{parentA, parentB}, "hash-a"),
			},
			wantDups: []string{orphanID},
			wantRest: []string{},
		},
		{
			name:     "no_match_kept_history_empty",
			fork:     candidate(t, forkID, []string{parent1}, "hash-a"),
			kept:     nil,
			wantDups: []string{},
			wantRest: []string{forkID},
		},
		{
			// Deliberate design choice: the kept sibling doesn't need a
			// description itself — the safety property only depends on the content
			// being preserved somewhere kept.
			name: "match_against_undescribed_kept",
			fork: candidate(t, orphanID, []string{parent1}, "hash-a"),
			kept: []classify.Candidate{{
				ChangeID:        "kept",
				ParentChangeIDs: []string{parent1},
				DiffHash:        "hash-a",
				Description:     "",
			}},
			wantDups: []string{orphanID},
			wantRest: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			duplicates, rest := classify.GitCommitDuplicates(
				[]classify.Candidate{tt.fork},
				tt.kept,
			)

			dupIDs := make([]string, len(duplicates))
			for i, d := range duplicates {
				dupIDs[i] = d.ChangeID
			}

			restIDs := make([]string, len(rest))
			for i, r := range rest {
				restIDs[i] = r.ChangeID
			}

			assert.Equal(t, tt.wantDups, dupIDs)
			assert.Equal(t, tt.wantRest, restIDs)
		})
	}
}
