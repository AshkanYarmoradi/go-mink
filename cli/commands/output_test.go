package commands

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHelpOutputColorCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name  string
		force string
		args  []string
	}{
		{name: "redirected help", args: []string{"--help"}},
		{name: "no-color help before pre-run", force: "1", args: []string{"--no-color", "--help"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLICOLOR_FORCE", tt.force)
			t.Setenv("NO_COLOR", "")
			cmd := NewRootCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.Execute())
			assert.Contains(t, out.String(), "Quick Start:")
			assert.NotContains(t, out.String(), "\x1b[")
		})
	}
}
