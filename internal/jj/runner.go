// Package jj wraps invocations of the jj CLI as a real subprocess.
package jj

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Runner executes jj commands. Implementations must treat a non-zero exit
// as an error, not as data to inspect.
type Runner interface {
	// Run executes jj with the given args and returns captured stdout.
	Run(ctx context.Context, args ...string) (stdout string, err error)
	// Stream executes jj with the given args, piping stdout straight to w.
	Stream(ctx context.Context, w io.Writer, args ...string) error
}

// ExecRunner is a Runner backed by exec.CommandContext against the real jj
// binary on PATH.
type ExecRunner struct {
	// Repository, if non-empty, is passed as jj's own `-R` flag on every
	// invocation, so jj-trim can operate on a repo outside the process's
	// cwd. Empty means jj's own default: search ancestors of cwd.
	Repository string
}

var _ Runner = ExecRunner{}

// Run implements Runner by executing the real jj binary and capturing its
// stdout.
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer

	if err := r.run(ctx, args, &stdout); err != nil {
		return "", err
	}

	return stdout.String(), nil
}

// Stream implements Runner by executing the real jj binary and piping its
// stdout straight to w.
func (r ExecRunner) Stream(ctx context.Context, w io.Writer, args ...string) error {
	return r.run(ctx, args, w)
}

// run is Run/Stream's shared implementation, differing only in where
// stdout is directed.
func (r ExecRunner) run(ctx context.Context, args []string, stdout io.Writer) error {
	var stderr bytes.Buffer

	//nolint:gosec // args are internally constructed by internal/jj's typed methods, never raw user/shell input
	cmd := exec.CommandContext(ctx, "jj", r.withRepository(args)...)
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("jj %v: %w: %s", args, err, stderr.String())
	}

	return nil
}

// withRepository prepends `-R <path>` when Repository is set.
func (r ExecRunner) withRepository(args []string) []string {
	if r.Repository == "" {
		return args
	}

	return append([]string{"-R", r.Repository}, args...)
}
