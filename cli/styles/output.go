package styles

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/charmbracelet/colorprofile"
)

var colorsDisabled atomic.Bool

// NewWriter adapts styled output to the destination's terminal capabilities.
// Lip Gloss v2 renders full ANSI colors; applying the profile at the output
// boundary keeps redirected output plain and honors NO_COLOR and --no-color.
func NewWriter(out io.Writer) *colorprofile.Writer {
	w := colorprofile.NewWriter(out, os.Environ())
	if colorsDisabled.Load() || os.Getenv("NO_COLOR") != "" {
		w.Profile = colorprofile.NoTTY
	}
	return w
}

// Println writes a line to stdout using its detected color profile.
func Println(args ...any) {
	_, _ = fmt.Fprintln(NewWriter(os.Stdout), args...)
}

// Printf writes formatted text to stdout using its detected color profile.
func Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(NewWriter(os.Stdout), format, args...)
}
