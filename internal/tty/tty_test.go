package tty

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFakeTerminal swaps the package-level isTerminal check for a fake
// keyed by *os.File identity, restoring the real one on cleanup — the
// only practical way to exercise Require's stdin/stdout logic without an
// actual pty, which CI doesn't reliably have attached. Not run in
// parallel with other tests in this package: it mutates shared package
// state.
func withFakeTerminal(t *testing.T, terminals map[*os.File]bool) {
	t.Helper()

	original := IsTerminal
	IsTerminal = func(f *os.File) bool { return terminals[f] }

	t.Cleanup(func() { IsTerminal = original })
}

func TestRequire(t *testing.T) {
	tests := []struct {
		name      string
		terminals map[*os.File]bool
		wantErrIs error
	}{
		{
			name:      "both terminals",
			terminals: map[*os.File]bool{os.Stdin: true, os.Stdout: true},
			wantErrIs: nil,
		},
		{
			name:      "stdin not terminal",
			terminals: map[*os.File]bool{os.Stdin: false, os.Stdout: true},
			wantErrIs: ErrNotInteractive,
		},
		{
			name:      "stdout not terminal",
			terminals: map[*os.File]bool{os.Stdin: true, os.Stdout: false},
			wantErrIs: ErrNotInteractive,
		},
		{
			name:      "neither terminal",
			terminals: map[*os.File]bool{os.Stdin: false, os.Stdout: false},
			wantErrIs: ErrNotInteractive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFakeTerminal(t, tt.terminals)

			err := Require(os.Stdin, os.Stdout)

			if tt.wantErrIs != nil {
				require.ErrorIs(t, err, tt.wantErrIs)

				return
			}

			assert.NoError(t, err)
		})
	}
}
