package preview_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hugoh/jj-trim/internal/classify"
	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/hugoh/jj-trim/internal/preview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const candidateLogLine = "@ w feature\n"

// failAfterWriter is an io.Writer that lets the first n Write calls succeed
// (recording their bytes into buf) and fails every call after that — used to
// pinpoint exactly which of Print's several sequential writes is the one
// whose error must propagate, since each Fprint*/Stream call here amounts to
// exactly one Write call on the underlying writer.
type failAfterWriter struct {
	n   int
	buf bytes.Buffer
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, errors.New("write failed")
	}

	w.n--

	n, err := w.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("failAfterWriter: %w", err)
	}

	return n, nil
}

func TestPrint_GraphThenLegend(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): candidateLogLine,
		},
	}

	var buf bytes.Buffer

	legend := []classify.LegendEntry{
		{ChangeIDShort: "w", Reason: classify.ReasonMerged},
	}

	err := preview.Print(context.Background(), fake, &buf, "candidates()", legend, false)
	require.NoError(t, err)
	assert.Equal(t,
		candidateLogLine+"\n"+
			"Legend: ([H]/[M]/[L] = confidence it's safe to delete: high/medium/low)\n"+
			"  w  [H] merged into trunk\n",
		buf.String())
}

func TestPrint_Explain(t *testing.T) {
	t.Parallel()

	fake := &jj.Fake{
		Stdout: map[string]string{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): candidateLogLine,
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
	expected := candidateLogLine + "\n" +
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

func TestPrint_GraphError_Propagates(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	fake := &jj.Fake{
		Errs: map[string]error{
			jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): boom,
		},
	}

	var buf bytes.Buffer

	err := preview.Print(context.Background(), fake, &buf, "candidates()", nil, false)
	require.ErrorIs(t, err, boom)
}

func TestPrint_WriteErrors_Propagate(t *testing.T) {
	t.Parallel()

	legend := []classify.LegendEntry{
		{ChangeIDShort: "w", Reason: classify.ReasonMerged},
	}

	tests := []struct {
		name        string
		explain     bool
		allowWrites int
	}{
		{name: "legend header write fails", explain: false, allowWrites: 1},
		{name: "legend entry write fails", explain: false, allowWrites: 2},
		{name: "details header write fails", explain: true, allowWrites: 3},
		{name: "details paragraph write fails", explain: true, allowWrites: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &jj.Fake{
				Stdout: map[string]string{
					jj.Key("log", "-r", "candidates()", "--no-pager", "--color=never"): candidateLogLine,
				},
			}
			w := &failAfterWriter{n: tt.allowWrites}

			err := preview.Print(context.Background(), fake, w, "candidates()", legend, tt.explain)
			require.Error(t, err)
		})
	}
}
