package styles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatSuccess(t *testing.T) {
	result := FormatSuccess("test message")
	assert.Contains(t, result, IconSuccess)
	assert.Contains(t, result, "test message")
}

func TestFormatError(t *testing.T) {
	result := FormatError("error message")
	assert.Contains(t, result, IconError)
	assert.Contains(t, result, "error message")
}

func TestFormatWarning(t *testing.T) {
	result := FormatWarning("warning message")
	assert.Contains(t, result, IconWarning)
	assert.Contains(t, result, "warning message")
}

func TestFormatInfo(t *testing.T) {
	result := FormatInfo("info message")
	assert.Contains(t, result, IconInfo)
	assert.Contains(t, result, "info message")
}

func TestFormatStep(t *testing.T) {
	result := FormatStep(1, 5, "doing something")
	assert.Contains(t, result, "doing something")
	assert.Contains(t, result, "[")
	assert.Contains(t, result, "]")
}

func TestFormatKeyValue(t *testing.T) {
	result := FormatKeyValue("Status", "Active")
	assert.Contains(t, result, "Status")
	assert.Contains(t, result, "Active")
}

func TestDisableColors(t *testing.T) {
	// Store original values
	originalPrimary := Primary
	originalSuccess := Success

	// Disable colors
	DisableColors()

	// Check colors are empty
	assert.Equal(t, "", string(Primary))
	assert.Equal(t, "", string(Success))

	// Restore original values for other tests
	Primary = originalPrimary
	Success = originalSuccess
}

func TestIcons(t *testing.T) {
	// Verify icons are defined
	assert.NotEmpty(t, IconSuccess)
	assert.NotEmpty(t, IconError)
	assert.NotEmpty(t, IconWarning)
	assert.NotEmpty(t, IconInfo)
	assert.NotEmpty(t, IconMink)
	assert.NotEmpty(t, IconPending)
	assert.NotEmpty(t, IconStream)
}

func TestStyles(t *testing.T) {
	// Test that styles can be rendered without panic
	assert.NotPanics(t, func() {
		_ = Bold.Render("test")
		_ = Title.Render("test")
		_ = Subtitle.Render("test")
		_ = Normal.Render("test")
		_ = Muted.Render("test")
		_ = Code.Render("test")
		_ = SuccessStyle.Render("test")
		_ = WarningStyle.Render("test")
		_ = ErrorStyle.Render("test")
		_ = InfoStyle.Render("test")
	})
}

func TestBoxStyles(t *testing.T) {
	// Test that box styles can be rendered without panic
	assert.NotPanics(t, func() {
		_ = Box.Render("test content")
		_ = BoxHighlight.Render("test content")
		_ = BoxSuccess.Render("test content")
		_ = BoxError.Render("test content")
		_ = BoxWarning.Render("test content")
		_ = InfoBox.Render("test content")
	})
}
