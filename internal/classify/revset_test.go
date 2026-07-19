package classify_test

import (
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/stretchr/testify/assert"
)

// TestMergedBookmarks_ExcludesTrunkItself is the first table-driven case
// DESIGN.md's correctness note calls for: jj's `::x` revset includes x
// itself, so a bookmark sitting exactly on trunk must not be classified
// merged. This test only checks the revset string is built with the
// exclusion; internal/jj's integration tests exercise it against a real
// repo (see TestLog_RealRepo_ProtectedTrunkBookmark).
func TestMergedBookmarks_ExcludesTrunkItself(t *testing.T) {
	t.Parallel()

	got := classify.MergedBookmarks("trunk()")
	assert.Equal(t, "bookmarks() & ::(trunk()) ~ (trunk())", got)
}

func TestMergedBookmarks_CustomTrunk(t *testing.T) {
	t.Parallel()

	got := classify.MergedBookmarks("main@origin")
	assert.Equal(t, "bookmarks() & ::(main@origin) ~ (main@origin)", got)
}

func TestUnmergedBookmarks(t *testing.T) {
	t.Parallel()

	got := classify.UnmergedBookmarks("trunk()")
	assert.Equal(t, "bookmarks() ~ ::(trunk()) ~ immutable()", got)
}

func TestAnonymousForks(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "heads(mutable()) ~ bookmarks() ~ @", classify.AnonymousForks())
}

func TestAnonymousForksNoDescription(t *testing.T) {
	t.Parallel()

	got := classify.AnonymousForksNoDescription()
	assert.Equal(t, `(heads(mutable()) ~ bookmarks() ~ @) & description("")`, got)
}

func TestChangeIDRevset(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "change_id(abc)", classify.ChangeIDRevset("abc"))
}

func TestDescendantsRevset_WrapsChangeID(t *testing.T) {
	t.Parallel()

	got := classify.DescendantsRevset("abc")
	assert.Equal(t, "descendants(change_id(abc)) & mutable() ~ change_id(abc)", got)
}

func TestKeepRevset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		trunk      string
		bookmarks  string
		extraRoots []string
		want       string
	}{
		{
			name:      "no extra roots",
			trunk:     "trunk()",
			bookmarks: "bookmarks()",
			want:      "::(trunk()) | ::(bookmarks()) | ::@",
		},
		{
			name:       "with extra roots",
			trunk:      "trunk()",
			bookmarks:  "bookmarks()",
			extraRoots: []string{"abc", "def"},
			want:       "::(trunk()) | ::(bookmarks()) | ::@ | ::(change_id(abc)) | ::(change_id(def))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify.KeepRevset(tt.trunk, tt.bookmarks, tt.extraRoots...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPrivateChainRevset(t *testing.T) {
	t.Parallel()

	got := classify.PrivateChainRevset("abc", "::(trunk()) | ::(bookmarks()) | ::@")
	assert.Equal(t, "::(change_id(abc)) ~ (::(trunk()) | ::(bookmarks()) | ::@)", got)
}
