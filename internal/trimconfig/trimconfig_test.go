package trimconfig_test

import (
	"testing"
	"time"

	"github.com/hugoh/jj-trim/internal/trimconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_ZeroValue(t *testing.T) {
	t.Parallel()

	var cfg trimconfig.Config

	assert.Empty(t, cfg.Repository)
	assert.False(t, cfg.Fetch)
	assert.Empty(t, cfg.Protected)
	assert.Empty(t, cfg.Trunk)
	assert.Nil(t, cfg.StaleAfter)
	assert.False(t, cfg.NoDescriptionOnly)
}

func TestConfig_Populated(t *testing.T) {
	t.Parallel()

	staleAfter := 30 * 24 * time.Hour
	cfg := trimconfig.Config{
		Repository:        "/some/repo",
		Fetch:             true,
		Protected:         []string{"release/*"},
		Trunk:             "main@origin",
		StaleAfter:        &staleAfter,
		NoDescriptionOnly: true,
	}

	assert.Equal(t, "/some/repo", cfg.Repository)
	assert.True(t, cfg.Fetch)
	assert.Equal(t, []string{"release/*"}, cfg.Protected)
	assert.Equal(t, "main@origin", cfg.Trunk)
	require.NotNil(t, cfg.StaleAfter)
	assert.Equal(t, 30*24*time.Hour, *cfg.StaleAfter)
	assert.True(t, cfg.NoDescriptionOnly)
}

func TestConfig_ZeroStaleAfterDistinctFromUnset(t *testing.T) {
	t.Parallel()

	zero := time.Duration(0)
	cfg := trimconfig.Config{StaleAfter: &zero}

	require.NotNil(t, cfg.StaleAfter)
	assert.Equal(t, time.Duration(0), *cfg.StaleAfter)
}
