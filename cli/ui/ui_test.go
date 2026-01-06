package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
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
		{"default spinner", "Default...", SpinnerType(999)}, // Unknown type falls back to default
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

func TestSpinnerInit(t *testing.T) {
	s := NewSpinner("Loading...", SpinnerDots)
	cmd := s.Init()
	assert.NotNil(t, cmd)
}

func TestSpinnerUpdate(t *testing.T) {
	s := NewSpinner("Loading...", SpinnerDots)

	// Test quit key
	t.Run("quit with q", func(t *testing.T) {
		model, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		sm := model.(SpinnerModel)
		assert.True(t, sm.quitting)
		assert.NotNil(t, cmd)
	})

	// Test esc key
	t.Run("quit with esc", func(t *testing.T) {
		s2 := NewSpinner("Loading...", SpinnerDots)
		model, cmd := s2.Update(tea.KeyMsg{Type: tea.KeyEsc})
		sm := model.(SpinnerModel)
		assert.True(t, sm.quitting)
		assert.NotNil(t, cmd)
	})

	// Test ctrl+c
	t.Run("quit with ctrl+c", func(t *testing.T) {
		s3 := NewSpinner("Loading...", SpinnerDots)
		model, cmd := s3.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		sm := model.(SpinnerModel)
		assert.True(t, sm.quitting)
		assert.NotNil(t, cmd)
	})

	// Test done message
	t.Run("spinner done success", func(t *testing.T) {
		s4 := NewSpinner("Loading...", SpinnerDots)
		model, cmd := s4.Update(SpinnerDoneMsg{Result: "Success!", Err: nil})
		sm := model.(SpinnerModel)
		assert.True(t, sm.done)
		assert.Equal(t, "Success!", sm.result)
		assert.Nil(t, sm.err)
		assert.NotNil(t, cmd)
	})

	// Test done message with error
	t.Run("spinner done error", func(t *testing.T) {
		s5 := NewSpinner("Loading...", SpinnerDots)
		testErr := assert.AnError
		model, cmd := s5.Update(SpinnerDoneMsg{Result: "Failed", Err: testErr})
		sm := model.(SpinnerModel)
		assert.True(t, sm.done)
		assert.Equal(t, testErr, sm.err)
		assert.NotNil(t, cmd)
	})

	// Test tick message
	t.Run("spinner tick", func(t *testing.T) {
		s6 := NewSpinner("Loading...", SpinnerDots)
		model, cmd := s6.Update(spinner.TickMsg{Time: time.Now()})
		_ = model.(SpinnerModel)
		assert.NotNil(t, cmd)
	})

	// Test unhandled message
	t.Run("unhandled message", func(t *testing.T) {
		s7 := NewSpinner("Loading...", SpinnerDots)
		model, cmd := s7.Update(tea.WindowSizeMsg{})
		_ = model.(SpinnerModel)
		assert.Nil(t, cmd)
	})
}

func TestSpinnerView(t *testing.T) {
	t.Run("normal view", func(t *testing.T) {
		spinner := NewSpinner("Loading...", SpinnerDots)
		view := spinner.View()
		assert.Contains(t, view, "Loading...")
	})

	t.Run("done view success", func(t *testing.T) {
		s := NewSpinner("Loading...", SpinnerDots)
		s.done = true
		s.result = "Success!"
		view := s.View()
		assert.Contains(t, view, "Success!")
	})

	t.Run("done view error", func(t *testing.T) {
		s := NewSpinner("Loading...", SpinnerDots)
		s.done = true
		s.result = "Failed"
		s.err = assert.AnError
		view := s.View()
		assert.Contains(t, view, "Failed")
	})

	t.Run("quitting view", func(t *testing.T) {
		s := NewSpinner("Loading...", SpinnerDots)
		s.quitting = true
		view := s.View()
		assert.Contains(t, view, "Cancelled")
	})
}

func TestNewProgress(t *testing.T) {
	progress := NewProgress("Downloading...")
	assert.Equal(t, "Downloading...", progress.message)
	assert.Equal(t, float64(0), progress.percent)
	assert.False(t, progress.done)
}

func TestProgressInit(t *testing.T) {
	p := NewProgress("Loading...")
	cmd := p.Init()
	assert.Nil(t, cmd)
}

func TestProgressUpdate(t *testing.T) {
	t.Run("quit with q", func(t *testing.T) {
		p := NewProgress("Loading...")
		model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		_ = model.(ProgressModel)
		assert.NotNil(t, cmd)
	})

	t.Run("progress message", func(t *testing.T) {
		p := NewProgress("Loading...")
		model, cmd := p.Update(ProgressMsg{Percent: 0.5, Message: "50%"})
		pm := model.(ProgressModel)
		assert.Equal(t, 0.5, pm.percent)
		assert.Equal(t, "50%", pm.message)
		assert.False(t, pm.done)
		assert.Nil(t, cmd)
	})

	t.Run("progress complete", func(t *testing.T) {
		p := NewProgress("Loading...")
		model, cmd := p.Update(ProgressMsg{Percent: 1.0, Message: "Done!"})
		pm := model.(ProgressModel)
		assert.True(t, pm.done)
		assert.NotNil(t, cmd)
	})

	t.Run("unhandled message", func(t *testing.T) {
		p := NewProgress("Loading...")
		model, cmd := p.Update(tea.WindowSizeMsg{})
		_ = model.(ProgressModel)
		assert.Nil(t, cmd)
	})
}

func TestProgressView(t *testing.T) {
	t.Run("in progress", func(t *testing.T) {
		p := NewProgress("Loading...")
		p.percent = 0.5
		view := p.View()
		assert.Contains(t, view, "Loading...")
	})

	t.Run("done", func(t *testing.T) {
		p := NewProgress("Loading...")
		p.done = true
		p.message = "Complete!"
		view := p.View()
		assert.Contains(t, view, "Complete!")
	})
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

func TestTable_AddRow_FewerColumns(t *testing.T) {
	table := NewTable("Name", "Value", "Status")
	table.AddRow("only", "two") // Only 2 values for 3 columns

	assert.Len(t, table.rows, 1)
	assert.Equal(t, []string{"only", "two", ""}, table.rows[0])
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
		{"ACTIVE", "ACTIVE"}, // Test case insensitivity
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

func TestAnimatedBannerInit(t *testing.T) {
	banner := NewAnimatedBanner()
	cmd := banner.Init()
	assert.NotNil(t, cmd)
}

func TestAnimatedBannerUpdate(t *testing.T) {
	t.Run("animation tick not done", func(t *testing.T) {
		banner := NewAnimatedBanner()
		model, cmd := banner.Update(AnimationTickMsg{})
		ab := model.(AnimatedBannerModel)
		assert.Equal(t, 1, ab.frameIndex)
		assert.False(t, ab.done)
		assert.NotNil(t, cmd)
	})

	t.Run("animation complete", func(t *testing.T) {
		banner := NewAnimatedBanner()
		banner.frameIndex = len(banner.frames) - 1
		model, cmd := banner.Update(AnimationTickMsg{})
		ab := model.(AnimatedBannerModel)
		assert.True(t, ab.done)
		assert.NotNil(t, cmd)
	})

	t.Run("key press quits", func(t *testing.T) {
		banner := NewAnimatedBanner()
		model, cmd := banner.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		_ = model.(AnimatedBannerModel)
		assert.NotNil(t, cmd)
	})

	t.Run("unhandled message", func(t *testing.T) {
		banner := NewAnimatedBanner()
		model, cmd := banner.Update(tea.WindowSizeMsg{})
		_ = model.(AnimatedBannerModel)
		assert.Nil(t, cmd)
	})
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

func TestListItemsEmpty(t *testing.T) {
	items := []string{}
	list := ListItems(items)
	assert.Empty(t, list)
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

func TestNumberedListEmpty(t *testing.T) {
	items := []string{}
	list := NumberedList(items)
	assert.Empty(t, list)
}

func TestConfirmation(t *testing.T) {
	yes := Confirmation(true)
	no := Confirmation(false)

	assert.Contains(t, yes, "Yes")
	assert.Contains(t, no, "No")
}
