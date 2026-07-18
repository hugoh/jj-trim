package classify

// GitCommitDuplicates splits candidates into those whose content exactly
// duplicates a commit in keptHistory (same DuplicateKey — same parent(s),
// same diff), and the rest. A match means the candidate's full content is
// already preserved verbatim in a commit that's definitely kept (reachable
// from the working copy or a bookmark — see KeptHistory), regardless of
// whether that kept commit happens to have a description: the safety
// property only depends on the content surviving somewhere kept, not on
// how that commit got there.
//
// This is almost always the artifact of running `git commit` directly in a
// colocated repo instead of `jj commit`/`jj describe`: jj's own prior
// working-copy commit is left behind as an orphaned sibling of the new
// commit git created, sharing the same parent and the same content but
// never described.
func GitCommitDuplicates(candidates, keptHistory []Candidate) ([]Candidate, []Candidate) {
	kept := make(map[string]bool, len(keptHistory))
	for _, c := range keptHistory {
		kept[c.DuplicateKey()] = true
	}

	var duplicates, rest []Candidate

	for _, c := range candidates {
		if kept[c.DuplicateKey()] {
			duplicates = append(duplicates, c)
		} else {
			rest = append(rest, c)
		}
	}

	return duplicates, rest
}
