package styles

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FormatStep used to build the counters with string(rune('0'+n)), which only
// works for 0..9 and folds anything larger into a single wrong rune (gosec
// G115). The counters are now rendered with strconv.Itoa.
func TestFormatStep_MultiDigitCounters(t *testing.T) {
	tests := []struct {
		step  int
		total int
		want  string
	}{
		{1, 5, "[1/5]"},
		{9, 9, "[9/9]"},
		{10, 12, "[10/12]"},
		{12, 99, "[12/99]"},
		{0, 10, "[0/10]"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_of_%d", tt.step, tt.total), func(t *testing.T) {
			got := FormatStep(tt.step, tt.total, "doing something")
			assert.Contains(t, got, tt.want)
			assert.Contains(t, got, "doing something")
		})
	}
}
