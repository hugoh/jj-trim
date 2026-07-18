package preview_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/preview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrint_GraphThenLegend(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): "@ w feature\n",
		},
	}

	var buf bytes.Buffer

	legend := []classify.LegendEntry{
		{ChangeIDShort: "w", Reason: classify.ReasonMerged},
	}

	err := preview.Print(context.Background(), fake, &buf, "candidates()", legend, false)
	require.NoError(t, err)
	assert.Equal(t,
		"@ w feature\n\n"+
			"Legend: ([H]/[M]/[L] = confidence it's safe to delete: high/medium/low)\n"+
			"  w  [H] merged into trunk\n",
		buf.String())
}

func TestPrint_Explain(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): "@ w feature\n",
		},
	}

	var buf bytes.Buffer

	legend := []classify.LegendEntry{
		{ChangeIDShort: "w", Reason: classify.ReasonMerged},
		{ChangeIDShort: "x", Reason: classify.ReasonMerged},
	}

	err := preview.Print(context.Background(), fake, &buf, "candidates()", legend, true)
	require.NoError(t, err)

	info := classify.Describe(classify.ReasonMerged)
	expected := "@ w feature\n\n" +
		"Legend: ([H]/[M]/[L] = confidence it's safe to delete: high/medium/low)\n" +
		"  w  [H] merged into trunk\n  x  [H] merged into trunk\n" +
		"\nDetails:\n  [H] merged into trunk\n      " + info.Long + "\n"
	assert.Equal(t, expected, buf.String())
}

func TestPrint_NoLegendWhenEmpty(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): "(no candidates)\n",
		},
	}

	var buf bytes.Buffer

	err := preview.Print(context.Background(), fake, &buf, "candidates()", nil, false)
	require.NoError(t, err)
	assert.Equal(t, "(no candidates)\n", buf.String())
}
