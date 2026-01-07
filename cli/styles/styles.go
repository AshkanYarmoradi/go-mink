// Package styles provides consistent styling for the go-mink CLI.
// It defines colors, fonts, and reusable style components for a beautiful UI.
package styles

import (
	"github.com/charmbracelet/lipgloss"
)

// Color palette - carefully chosen for accessibility and aesthetics
var (
	// Primary colors
	Primary       = lipgloss.Color("#7C3AED") // Vibrant purple
	PrimaryLight  = lipgloss.Color("#A78BFA") // Light purple
	PrimaryDark   = lipgloss.Color("#5B21B6") // Dark purple
	Secondary     = lipgloss.Color("#06B6D4") // Cyan
	SecondaryDark = lipgloss.Color("#0891B2") // Dark cyan

	// Status colors
	Success      = lipgloss.Color("#10B981") // Emerald green
	SuccessLight = lipgloss.Color("#34D399") // Light green
	Warning      = lipgloss.Color("#F59E0B") // Amber
	WarningLight = lipgloss.Color("#FBBF24") // Light amber
	Error        = lipgloss.Color("#EF4444") // Red
	ErrorLight   = lipgloss.Color("#F87171") // Light red
	Info         = lipgloss.Color("#3B82F6") // Blue
	InfoLight    = lipgloss.Color("#60A5FA") // Light blue

	// Neutral colors
	Text       = lipgloss.Color("#F9FAFB") // Almost white
	TextMuted  = lipgloss.Color("#9CA3AF") // Gray
	TextDim    = lipgloss.Color("#6B7280") // Darker gray
	Background = lipgloss.Color("#111827") // Dark background
	Surface    = lipgloss.Color("#1F2937") // Slightly lighter
	Border     = lipgloss.Color("#374151") // Border gray

	// Accent colors for variety
	Accent1 = lipgloss.Color("#EC4899") // Pink
	Accent2 = lipgloss.Color("#8B5CF6") // Purple
	Accent3 = lipgloss.Color("#14B8A6") // Teal
)

// Text styles
var (
	// Bold text in primary color
	Bold = lipgloss.NewStyle().
		Bold(true)

	// Title style for headers
	Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(Primary).
		MarginBottom(1)

	// Subtitle for secondary headers
	Subtitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(PrimaryLight)

	// Normal text
	Normal = lipgloss.NewStyle().
		Foreground(Text)

	// Muted text for less important info
	Muted = lipgloss.NewStyle().
		Foreground(TextMuted)

	// Dim text for very subtle info
	Dim = lipgloss.NewStyle().
		Foreground(TextDim)

	// Highlight for important text
	Highlight = lipgloss.NewStyle().
			Bold(true).
			Foreground(Secondary)

	// Code style for inline code
	Code = lipgloss.NewStyle().
		Foreground(WarningLight).
		Background(Surface).
		Padding(0, 1)
)

// newStatusStyle creates a style with the given foreground color.
func newStatusStyle(color lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(color)
}

// newStatusBoldStyle creates a bold style with the given foreground color.
func newStatusBoldStyle(color lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(color)
}

// Status styles - using helper functions to reduce duplication
var (
	SuccessStyle = newStatusStyle(Success)     // SuccessStyle for success messages
	SuccessBold  = newStatusBoldStyle(Success) // SuccessBold for emphasized success
	WarningStyle = newStatusStyle(Warning)     // WarningStyle for warning messages
	WarningBold  = newStatusBoldStyle(Warning) // WarningBold for emphasized warnings
	ErrorStyle   = newStatusStyle(Error)       // ErrorStyle for error messages
	ErrorBold    = newStatusBoldStyle(Error)   // ErrorBold for emphasized errors
	InfoStyle    = newStatusStyle(Info)        // InfoStyle for informational messages
	InfoBold     = newStatusBoldStyle(Info)    // InfoBold for emphasized info
)

// Icons - using Unicode symbols for beautiful indicators
const (
	IconSuccess  = "✓"
	IconError    = "✗"
	IconWarning  = "⚠"
	IconInfo     = "ℹ"
	IconArrow    = "→"
	IconDot      = "•"
	IconCheck    = "✔"
	IconCross    = "✘"
	IconStar     = "★"
	IconHeart    = "♥"
	IconSparkle  = "✨"
	IconRocket   = "🚀"
	IconPackage  = "📦"
	IconFolder   = "📁"
	IconFile     = "📄"
	IconDatabase = "🗄️"
	IconGear     = "⚙️"
	IconLock     = "🔒"
	IconKey      = "🔑"
	IconMink     = "🦫" // Mink emoji (beaver closest match)
)

// newRoundedBox creates a box style with rounded border and specified border color.
func newRoundedBox(borderColor lipgloss.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(1, 2)
}

// Box styles for containers
var (
	Box          = newRoundedBox(Border)  // Box with a subtle border
	BoxHighlight = newRoundedBox(Primary) // BoxHighlight with primary color border
	BoxSuccess   = newRoundedBox(Success) // BoxSuccess with success color border
	BoxError     = newRoundedBox(Error)   // BoxError with error color border
	BoxWarning   = newRoundedBox(Warning) // BoxWarning with warning color border
)

// Component styles
var (
	// MenuItem for menu items
	MenuItem = lipgloss.NewStyle().
			PaddingLeft(2)

	// MenuItemSelected for selected menu items
	MenuItemSelected = lipgloss.NewStyle().
				Foreground(Primary).
				Bold(true).
				PaddingLeft(2)

	// ListItem for list items
	ListItem = lipgloss.NewStyle().
			PaddingLeft(2).
			Foreground(Text)

	// ListItemBullet for list item bullets
	ListItemBullet = lipgloss.NewStyle().
			Foreground(Primary).
			PaddingRight(1)
)

// Layout helpers
var (
	// Indent for indented text
	Indent = lipgloss.NewStyle().
		PaddingLeft(2)

	// DoubleIndent for double indented text
	DoubleIndent = lipgloss.NewStyle().
			PaddingLeft(4)

	// Section for section content
	Section = lipgloss.NewStyle().
		MarginTop(1).
		MarginBottom(1)
)

// formatMessage is a helper that formats a message with an icon using the given style.
func formatMessage(style lipgloss.Style, icon, msg string) string {
	return style.Render(icon) + " " + Normal.Render(msg)
}

// FormatSuccess formats a success message with icon
func FormatSuccess(msg string) string { return formatMessage(SuccessStyle, IconSuccess, msg) }

// FormatError formats an error message with icon
func FormatError(msg string) string { return formatMessage(ErrorStyle, IconError, msg) }

// FormatWarning formats a warning message with icon
func FormatWarning(msg string) string { return formatMessage(WarningStyle, IconWarning, msg) }

// FormatInfo formats an info message with icon
func FormatInfo(msg string) string { return formatMessage(InfoStyle, IconInfo, msg) }

// FormatStep formats a step in a process
func FormatStep(step int, total int, msg string) string {
	stepStyle := lipgloss.NewStyle().
		Foreground(TextMuted).
		Width(8)
	return stepStyle.Render("["+string(rune('0'+step))+"/"+string(rune('0'+total))+"]") + " " + msg
}

// FormatKeyValue formats a key-value pair
func FormatKeyValue(key, value string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(TextMuted).
		Width(20)
	return keyStyle.Render(key+":") + " " + Highlight.Render(value)
}

// Additional icons
const (
	IconPending = "◌"
	IconStream  = "⇶"
	IconList    = "☰"
	IconChart   = "📊"
	IconHealth  = "❤️"
)

// InfoBox style for information boxes
var InfoBox = newRoundedBox(Info).MarginTop(1)

// allColorPointers returns pointers to all color variables for bulk operations.
func allColorPointers() []*lipgloss.Color {
	return []*lipgloss.Color{
		&Primary, &PrimaryLight, &PrimaryDark,
		&Secondary, &SecondaryDark,
		&Success, &SuccessLight,
		&Warning, &WarningLight,
		&Error, &ErrorLight,
		&Info, &InfoLight,
		&Text, &TextMuted, &TextDim,
		&Background, &Surface, &Border,
		&Accent1, &Accent2, &Accent3,
	}
}

// DisableColors disables all colors for terminals that don't support them
func DisableColors() {
	noColor := lipgloss.Color("")
	for _, c := range allColorPointers() {
		*c = noColor
	}
}
