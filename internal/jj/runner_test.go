package jj_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/hugoh/jj-trim/internal/jj"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecRunner_Run_Success(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := initRepo(t)
	r := jj.ExecRunner{Repository: dir}

	out, err := r.Run(context.Background(), "log", "-r", "@", "--no-graph", "-T", `"ok\n"`)
	require.NoError(t, err)
	assert.Equal(t, "ok\n", out)
}

func TestExecRunner_Run_Failure(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := initRepo(t)
	r := jj.ExecRunner{Repository: dir}

	_, err := r.Run(context.Background(), "log", "-r", "not-a-valid-revset(((")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj [log -r not-a-valid-revset(((]")
}

func TestExecRunner_Stream_WritesToWriter(t *testing.T) {
	t.Parallel()
	requireJJ(t)

	dir := initRepo(t)
	r := jj.ExecRunner{Repository: dir}

	var buf bytes.Buffer

	err := r.Stream(
		context.Background(),
		&buf,
		"log",
		"-r",
		"@",
		"--no-graph",
		"-T",
		`"streamed\n"`,
	)
	require.NoError(t, err)
	assert.Equal(t, "streamed\n", buf.String())
}

// TestExecRunner_WithRepository_TargetsRepoFromDifferentCWD guards
// withRepository's -R flag: an ExecRunner scoped to dir must still target
// dir's repo when the process's own cwd is a different directory entirely
// (here, another empty temp dir with no jj repo of its own), proving the
// -R flag - not an ambient cwd search - is what makes this work.
// Deliberately not t.Parallel(): t.Chdir forbids it.
func TestExecRunner_WithRepository_TargetsRepoFromDifferentCWD(t *testing.T) {
	requireJJ(t)

	dir := initRepo(t)
	elsewhere := t.TempDir()

	t.Chdir(elsewhere)

	r := jj.ExecRunner{Repository: dir}

	out, err := r.Run(context.Background(), "log", "-r", "@", "--no-graph",
		"-T", `self.description()`)
	require.NoError(t, err)
	assert.Contains(t, out, "root change")
}

func TestExecRunner_Run_NoRepository_UsesCWD(t *testing.T) {
	requireJJ(t)

	dir := initRepo(t)

	t.Chdir(dir)

	r := jj.ExecRunner{}

	out, err := r.Run(context.Background(), "log", "-r", "@", "--no-graph", "-T", `"cwd-scoped\n"`)
	require.NoError(t, err)
	assert.Equal(t, "cwd-scoped\n", out)
}
