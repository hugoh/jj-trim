package jj_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireIntegration(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{name: "unset", set: false, want: false},
		{name: "enabled", set: true, val: "1", want: true},
		{name: "other value", set: true, val: "true", want: false},
		{name: "empty", set: true, val: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REQUIRE_INTEGRATION", "sentinel")

			if tt.set {
				t.Setenv("REQUIRE_INTEGRATION", tt.val)
			} else {
				require.NoError(t, os.Unsetenv("REQUIRE_INTEGRATION"))
			}

			require.Equal(t, tt.want, requireIntegration())
		})
	}
}
