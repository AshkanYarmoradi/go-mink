// Package ui provides reusable UI components for the go-mink CLI.
// It includes spinners, progress bars, tables, and other interactive elements.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerType defines different spinner animations
type SpinnerType int

const (
	SpinnerDots SpinnerType = iota
	SpinnerLine
	SpinnerMinidots
	SpinnerJump
	SpinnerPulse
	SpinnerPoints
	SpinnerGlobe
	SpinnerMoon
	SpinnerMonkey
	SpinnerMeter
	SpinnerHamburger
)

// SpinnerModel is a spinner component with a message
type SpinnerModel struct {
	spinner  spinner.Model
	message  string
	quitting bool
	done     bool
	result   string
	err      error
}

// NewSpinner creates a new spinner with the given message
func NewSpinner(message string, spinnerType SpinnerType) SpinnerModel {
	s := spinner.New()

	switch spinnerType {
	case SpinnerDots:
		s.Spinner = spinner.Dot
	case SpinnerLine:
		s.Spinner = spinner.Line
	case SpinnerMinidots:
		s.Spinner = spinner.MiniDot
	case SpinnerJump:
		s.Spinner = spinner.Jump
	case SpinnerPulse:
		s.Spinner = spinner.Pulse
	case SpinnerPoints:
		s.Spinner = spinner.Points
	case SpinnerGlobe:
		s.Spinner = spinner.Globe
	case SpinnerMoon:
		s.Spinner = spinner.Moon
	case SpinnerMonkey:
		s.Spinner = spinner.Monkey
	case SpinnerMeter:
		s.Spinner = spinner.Meter
	case SpinnerHamburger:
		s.Spinner = spinner.Hamburger
	default:
		s.Spinner = spinner.Dot
	}

	s.Style = lipgloss.NewStyle().Foreground(styles.Primary)

	return SpinnerModel{
		spinner: s,
		message: message,
	}
}

func (m SpinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m SpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}

	case SpinnerDoneMsg:
		m.done = true
		m.result = msg.Result
		m.err = msg.Err
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m SpinnerModel) View() string {
	if m.done {
		if m.err != nil {
			return styles.FormatError(m.result) + "\n"
		}
		return styles.FormatSuccess(m.result) + "\n"
	}

	if m.quitting {
		return styles.FormatWarning("Cancelled") + "\n"
	}

	return m.spinner.View() + " " + styles.Normal.Render(m.message) + "\n"
}

// SpinnerDoneMsg signals that the spinner operation is complete
type SpinnerDoneMsg struct {
	Result string
	Err    error
}

// ProgressModel is a progress bar component
type ProgressModel struct {
	progress progress.Model
	percent  float64
	message  string
	done     bool
}

// NewProgress creates a new progress bar
func NewProgress(message string) ProgressModel {
	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(40),
		progress.WithoutPercentage(),
	)

	return ProgressModel{
		progress: p,
		message:  message,
	}
}

func (m ProgressModel) Init() tea.Cmd {
	return nil
}

func (m ProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}

	case ProgressMsg:
		m.percent = msg.Percent
		m.message = msg.Message
		if m.percent >= 1.0 {
			m.done = true
			return m, tea.Quit
		}
		return m, nil

	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd
	}

	return m, nil
}

func (m ProgressModel) View() string {
	if m.done {
		return styles.FormatSuccess(m.message) + "\n"
	}

	return m.progress.ViewAs(m.percent) + " " + styles.Muted.Render(m.message) + "\n"
}

// ProgressMsg updates the progress bar
type ProgressMsg struct {
	Percent float64
	Message string
}

// Table renders a beautiful table
type Table struct {
	headers []string
	rows    [][]string
	widths  []int
}

// NewTable creates a new table with headers
func NewTable(headers ...string) *Table {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	return &Table{
		headers: headers,
		rows:    make([][]string, 0),
		widths:  widths,
	}
}

// AddRow adds a row to the table
func (t *Table) AddRow(values ...string) {
	// Ensure we have the right number of columns
	row := make([]string, len(t.headers))
	for i := 0; i < len(t.headers); i++ {
		if i < len(values) {
			row[i] = values[i]
			if len(values[i]) > t.widths[i] {
				t.widths[i] = len(values[i])
			}
		}
	}
	t.rows = append(t.rows, row)
}

// Render returns the formatted table string
func (t *Table) Render() string {
	if len(t.headers) == 0 {
		return ""
	}

	var sb strings.Builder

	// Calculate total width
	totalWidth := 1 // Start with left border
	for _, w := range t.widths {
		totalWidth += w + 3 // column + padding + separator
	}

	// Header styles
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(styles.Primary).
		Padding(0, 1)

	cellStyle := lipgloss.NewStyle().
		Foreground(styles.Text).
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Foreground(styles.Border)

	// Top border
	sb.WriteString(borderStyle.Render("┌"))
	for i, w := range t.widths {
		sb.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
		if i < len(t.widths)-1 {
			sb.WriteString(borderStyle.Render("┬"))
		}
	}
	sb.WriteString(borderStyle.Render("┐"))
	sb.WriteString("\n")

	// Header row
	sb.WriteString(borderStyle.Render("│"))
	for i, h := range t.headers {
		cell := headerStyle.Width(t.widths[i]).Render(h)
		sb.WriteString(cell)
		sb.WriteString(borderStyle.Render("│"))
	}
	sb.WriteString("\n")

	// Header separator
	sb.WriteString(borderStyle.Render("├"))
	for i, w := range t.widths {
		sb.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
		if i < len(t.widths)-1 {
			sb.WriteString(borderStyle.Render("┼"))
		}
	}
	sb.WriteString(borderStyle.Render("┤"))
	sb.WriteString("\n")

	// Data rows
	for _, row := range t.rows {
		sb.WriteString(borderStyle.Render("│"))
		for i, cell := range row {
			c := cellStyle.Width(t.widths[i]).Render(cell)
			sb.WriteString(c)
			sb.WriteString(borderStyle.Render("│"))
		}
		sb.WriteString("\n")
	}

	// Bottom border
	sb.WriteString(borderStyle.Render("└"))
	for i, w := range t.widths {
		sb.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
		if i < len(t.widths)-1 {
			sb.WriteString(borderStyle.Render("┴"))
		}
	}
	sb.WriteString(borderStyle.Render("┘"))

	return sb.String()
}

// StatusBadge returns a styled status badge
func StatusBadge(status string) string {
	switch strings.ToLower(status) {
	case "active", "running", "healthy", "ok", "success", "applied":
		return lipgloss.NewStyle().
			Background(styles.Success).
			Foreground(lipgloss.Color("#000000")).
			Padding(0, 1).
			Render(status)
	case "pending", "paused", "waiting":
		return lipgloss.NewStyle().
			Background(styles.Warning).
			Foreground(lipgloss.Color("#000000")).
			Padding(0, 1).
			Render(status)
	case "error", "failed", "stopped":
		return lipgloss.NewStyle().
			Background(styles.Error).
			Foreground(lipgloss.Color("#FFFFFF")).
			Padding(0, 1).
			Render(status)
	default:
		return lipgloss.NewStyle().
			Background(styles.Surface).
			Foreground(styles.Text).
			Padding(0, 1).
			Render(status)
	}
}

// Banner renders the mink ASCII art banner
func Banner() string {
	banner := `
    ████████████████████████████████████████████
    █                                          █
    █   ███╗   ███╗██╗███╗   ██╗██╗  ██╗      █
    █   ████╗ ████║██║████╗  ██║██║ ██╔╝      █
    █   ██╔████╔██║██║██╔██╗ ██║█████╔╝       █
    █   ██║╚██╔╝██║██║██║╚██╗██║██╔═██╗       █
    █   ██║ ╚═╝ ██║██║██║ ╚████║██║  ██╗      █
    █   ╚═╝     ╚═╝╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝      █
    █                                          █
    █      Event Sourcing Toolkit for Go       █
    █                                          █
    ████████████████████████████████████████████
`
	return lipgloss.NewStyle().
		Foreground(styles.Primary).
		Bold(true).
		Render(banner)
}

// SimpleBanner returns a smaller, simpler banner
func SimpleBanner() string {
	banner := styles.IconMink + " " + lipgloss.NewStyle().
		Bold(true).
		Foreground(styles.Primary).
		Render("mink") +
		" " +
		styles.Muted.Render("- Event Sourcing Toolkit for Go")

	return banner
}

// AnimatedBanner creates an animated banner effect
type AnimatedBannerModel struct {
	frames     []string
	frameIndex int
	done       bool
}

func NewAnimatedBanner() AnimatedBannerModel {
	frames := []string{
		styles.IconMink,
		styles.IconMink + " m",
		styles.IconMink + " mi",
		styles.IconMink + " min",
		styles.IconMink + " mink",
	}
	return AnimatedBannerModel{
		frames: frames,
	}
}

func (m AnimatedBannerModel) Init() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return AnimationTickMsg{}
	})
}

type AnimationTickMsg struct{}

func (m AnimatedBannerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case AnimationTickMsg:
		if m.frameIndex < len(m.frames)-1 {
			m.frameIndex++
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return AnimationTickMsg{}
			})
		}
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m AnimatedBannerModel) View() string {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(styles.Primary)
	return titleStyle.Render(m.frames[m.frameIndex]) + "\n"
}

// Divider returns a horizontal divider line
func Divider(width int) string {
	return styles.Dim.Render(strings.Repeat("─", width))
}

// ListItems formats a list of items with bullets
func ListItems(items []string) string {
	var sb strings.Builder
	for _, item := range items {
		sb.WriteString(styles.ListItemBullet.Render(styles.IconDot))
		sb.WriteString(styles.ListItem.Render(item))
		sb.WriteString("\n")
	}
	return sb.String()
}

// NumberedList formats a numbered list
func NumberedList(items []string) string {
	var sb strings.Builder
	for i, item := range items {
		numStyle := lipgloss.NewStyle().
			Foreground(styles.Primary).
			Width(4)
		sb.WriteString(numStyle.Render(fmt.Sprintf("%d.", i+1)))
		sb.WriteString(styles.Normal.Render(item))
		sb.WriteString("\n")
	}
	return sb.String()
}

// Confirmation returns a yes/no prompt result display
func Confirmation(confirmed bool) string {
	if confirmed {
		return styles.SuccessStyle.Render("Yes")
	}
	return styles.ErrorStyle.Render("No")
}
