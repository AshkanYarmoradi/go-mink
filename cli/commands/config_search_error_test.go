package commands

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A missing mink.yaml keeps the familiar wording; an aborted (fail-closed) search
// must surface its own error so an operator never mistakes a permission failure
// for an absent file.
func TestConfigSearchError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantNotFound bool
	}{
		{name: "not exist is reported as no mink.yaml", err: os.ErrNotExist, wantNotFound: true},
		{name: "wrapped not exist is reported as no mink.yaml", err: fmt.Errorf("stat x: %w", os.ErrNotExist), wantNotFound: true},
		{name: "aborted search passes through unchanged", err: fmt.Errorf("config search aborted: %w", os.ErrPermission)},
		{name: "arbitrary error passes through unchanged", err: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configSearchError(tt.err)
			require.Error(t, got)
			assert.True(t, errors.Is(got, tt.err))
			if tt.wantNotFound {
				assert.Contains(t, got.Error(), "no mink.yaml found")
				assert.True(t, errors.Is(got, os.ErrNotExist))
			} else {
				assert.NotContains(t, got.Error(), "no mink.yaml found")
				assert.Equal(t, tt.err, got, "an aborted search must not be re-labelled")
			}
		})
	}
}
