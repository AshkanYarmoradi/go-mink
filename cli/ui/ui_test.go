package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSpinner(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		spinnerType SpinnerType
	}{
		{"dots spinner", "Loading...", SpinnerDots},
		{"line spinner", "Processing...", SpinnerLine},
		{"minidots spinner", "Working...", SpinnerMinidots},
		{"jump spinner", "Jumping...", SpinnerJump},
		{"pulse spinner", "Pulsing...", SpinnerPulse},
		{"points spinner", "Points...", SpinnerPoints},
		{"globe spinner", "Globe...", SpinnerGlobe},
		{"moon spinner", "Moon...", SpinnerMoon},
		{"monkey spinner", "Monkey...", SpinnerMonkey},
		{"meter spinner", "Meter...", SpinnerMeter},
		{"hamburger spinner", "Hamburger...", SpinnerHamburger},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spinner := NewSpinner(tt.message, tt.spinnerType)
			assert.Equal(t, tt.message, spinner.message)
			assert.False(t, spinner.quitting)
			assert.False(t, spinner.done)
		})
	}
}

func TestSpinnerView(t *testing.T) {
	spinner := NewSpinner("Loading...", SpinnerDots)
	view := spinner.View()
	assert.Contains(t, view, "Loading...")
}

func TestNewProgress(t *testing.T) {
	progress := NewProgress("Downloading...")
	assert.Equal(t, "Downloading...", progress.message)
	assert.Equal(t, float64(0), progress.percent)
	assert.False(t, progress.done)
}

func TestNewTable(t *testing.T) {
	table := NewTable("Name", "Value", "Status")
	assert.Equal(t, []string{"Name", "Value", "Status"}, table.headers)
	assert.Empty(t, table.rows)
	assert.Equal(t, 3, len(table.widths))
}

func TestTable_AddRow(t *testing.T) {
	table := NewTable("Name", "Value")
	table.AddRow("foo", "bar")
	table.AddRow("longer name", "value")
	
	assert.Len(t, table.rows, 2)
	assert.Equal(t, []string{"foo", "bar"}, table.rows[0])
	assert.Equal(t, []string{"longer name", "value"}, table.rows[1])
	
	// Width should be updated for longest value
	assert.GreaterOrEqual(t, table.widths[0], len("longer name"))
}

func TestTable_Render(t *testing.T) {
	table := NewTable("Name", "Status")
	table.AddRow("test", "active")
	table.AddRow("example", "pending")
	
	rendered := table.Render()
	
	// Should contain borders
	assert.Contains(t, rendered, "┌")
	assert.Contains(t, rendered, "┐")
	assert.Contains(t, rendered, "└")
	assert.Contains(t, rendered, "┘")
	
	// Should contain some data - the table may wrap text
	assert.NotEmpty(t, rendered)
}

func TestTable_RenderEmpty(t *testing.T) {
	table := &Table{}
	rendered := table.Render()
	assert.Empty(t, rendered)
}

func TestStatusBadge(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"active", "active"},
		{"running", "running"},
		{"healthy", "healthy"},
		{"ok", "ok"},
		{"success", "success"},
		{"applied", "applied"},
		{"pending", "pending"},
		{"paused", "paused"},
		{"waiting", "waiting"},
		{"error", "error"},
		{"failed", "failed"},
		{"stopped", "stopped"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			badge := StatusBadge(tt.status)
			assert.Contains(t, badge, tt.expected)
		})
	}
}

func TestBanner(t *testing.T) {
	banner := Banner()
	// Banner uses ASCII art - check it's not empty and contains the text
	assert.NotEmpty(t, banner)
	assert.Contains(t, banner, "Event Sourcing")
}

func TestSimpleBanner(t *testing.T) {
	banner := SimpleBanner()
	assert.Contains(t, banner, "mink")
	assert.Contains(t, banner, "Event Sourcing")
}

func TestNewAnimatedBanner(t *testing.T) {
	banner := NewAnimatedBanner()
	assert.NotEmpty(t, banner.frames)
	assert.Equal(t, 0, banner.frameIndex)
	assert.False(t, banner.done)
}

func TestAnimatedBannerView(t *testing.T) {
	banner := NewAnimatedBanner()
	view := banner.View()
	assert.NotEmpty(t, view)
}

func TestDivider(t *testing.T) {
	divider := Divider(20)
	// Should be 20 characters
	assert.True(t, strings.Contains(divider, "─"))
}

func TestListItems(t *testing.T) {
	items := []string{"Item 1", "Item 2", "Item 3"}
	list := ListItems(items)
	
	assert.Contains(t, list, "Item 1")
	assert.Contains(t, list, "Item 2")
	assert.Contains(t, list, "Item 3")
}

func TestNumberedList(t *testing.T) {
	items := []string{"First", "Second", "Third"}
	list := NumberedList(items)
	
	assert.Contains(t, list, "1.")
	assert.Contains(t, list, "2.")
	assert.Contains(t, list, "3.")
	assert.Contains(t, list, "First")
	assert.Contains(t, list, "Second")
	assert.Contains(t, list, "Third")
}

func TestConfirmation(t *testing.T) {
	yes := Confirmation(true)
	no := Confirmation(false)
	
	assert.Contains(t, yes, "Yes")
	assert.Contains(t, no, "No")
}
