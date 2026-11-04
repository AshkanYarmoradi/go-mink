package styles

import (
	"bytes"
	"fmt"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputWriterProfiles(t *testing.T) {
	for _, tt := range []struct {
		name     string
		force    string
		noColor  string
		disabled bool
		wantANSI bool
	}{
		{name: "redirected output is plain"},
		{name: "forced color is preserved", force: "1", wantANSI: true},
		{name: "NO_COLOR overrides forced color", force: "1", noColor: "1"},
		{name: "no-color flag overrides forced color", force: "1", disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLICOLOR_FORCE", tt.force)
			t.Setenv("NO_COLOR", tt.noColor)
			t.Setenv("CLICOLOR", "")
			previous := colorsDisabled.Swap(tt.disabled)
			t.Cleanup(func() { colorsDisabled.Store(previous) })

			var output bytes.Buffer
			styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Render("hello")
			require.Contains(t, styled, "\x1b[", "verify the v2 renderer supplied ANSI")
			_, err := fmt.Fprintln(NewWriter(&output), styled)
			require.NoError(t, err)
			if tt.wantANSI {
				assert.Contains(t, output.String(), "\x1b[")
			} else {
				assert.Equal(t, "hello\n", output.String())
			}
		})
	}
}

func TestOutputWriterRetainsTerminalProfile(t *testing.T) {
	// Color conversion belongs at the output boundary, where an ANSI-only
	// terminal must receive basic colors instead of unsupported RGB sequences.
	var output bytes.Buffer
	w := NewWriter(&output)
	w.Profile = colorprofile.ANSI
	_, err := fmt.Fprint(w, lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("hello"))
	require.NoError(t, err)
	assert.Contains(t, output.String(), "\x1b[")
	assert.NotContains(t, output.String(), "38;2;")
	assert.Contains(t, output.String(), "hello")
}
