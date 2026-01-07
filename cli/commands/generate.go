package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/AshkanYarmoradi/go-mink/cli/styles"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

// promptInput runs an interactive input form and returns the entered value.
// It only prompts if nonInteractive is false and the current value is empty.
func promptInput(title, description, placeholder string, value *string, nonInteractive bool) error {
	if nonInteractive || *value != "" {
		return nil
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title(title).Description(description).Value(value).Placeholder(placeholder),
		),
	).WithTheme(huh.ThemeDracula())
	return form.Run()
}

// parseCommaSeparated splits a comma-separated string into trimmed parts.
func parseCommaSeparated(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// NewGenerateCommand creates the generate command
func NewGenerateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate code scaffolding",
		Long: `Generate boilerplate code for aggregates, events, projections, and commands.

Examples:
  mink generate aggregate Order
  mink generate event OrderCreated --aggregate Order
  mink generate projection OrderSummary
  mink generate command CreateOrder --aggregate Order`,
		Aliases: []string{"gen", "g"},
	}

	cmd.AddCommand(newGenerateAggregateCommand())
	cmd.AddCommand(newGenerateEventCommand())
	cmd.AddCommand(newGenerateProjectionCommand())
	cmd.AddCommand(newGenerateCommandCommand())

	return cmd
}

func newGenerateAggregateCommand() *cobra.Command {
	var events []string
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:   "aggregate <name>",
		Short: "Generate an aggregate with events",
		Long: `Generate a new aggregate with optional initial events.

Examples:
  mink generate aggregate Order
  mink generate aggregate Order --events Created,ItemAdded,Shipped`,
		Aliases: []string{"agg", "a"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			// Interactive event selection if none provided
			if len(events) == 0 {
				var eventsInput string
				if err := promptInput("Events", "Comma-separated list of events (e.g., Created,Updated,Deleted)",
					"Created,Updated,Deleted", &eventsInput, nonInteractive); err != nil {
					return err
				}
				events = parseCommaSeparated(eventsInput)
			}

			data := AggregateData{
				Name:    toPascalCase(name),
				Module:  cfg.Project.Module,
				Package: filepath.Base(cfg.Generation.AggregatePackage),
				Events:  make([]EventData, 0, len(events)),
			}
			for _, e := range events {
				data.Events = append(data.Events, EventData{
					Name:          toPascalCase(e),
					AggregateName: data.Name,
				})
			}

			// Create aggregate file
			aggDir := cfg.Generation.AggregatePackage
			if err := os.MkdirAll(aggDir, 0755); err != nil {
				return err
			}

			aggFile := filepath.Join(aggDir, strings.ToLower(name)+".go")
			if err := generateFile(aggFile, aggregateTemplate, data); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", aggFile)))

			// Create events file if events provided
			if len(events) > 0 {
				eventsDir := cfg.Generation.EventPackage
				if err := os.MkdirAll(eventsDir, 0755); err != nil {
					return err
				}

				eventsFile := filepath.Join(eventsDir, strings.ToLower(name)+"_events.go")
				eventFileData := EventFileData{
					Module:    cfg.Project.Module,
					Package:   filepath.Base(cfg.Generation.EventPackage),
					Aggregate: data.Name,
					Events:    data.Events,
				}
				if err := generateFile(eventsFile, eventsFileTemplate, eventFileData); err != nil {
					return err
				}
				fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", eventsFile)))
			}

			// Create test file
			testFile := filepath.Join(aggDir, strings.ToLower(name)+"_test.go")
			if err := generateFile(testFile, aggregateTestTemplate, data); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", testFile)))

			fmt.Println()
			fmt.Println(styles.InfoBox.Render(fmt.Sprintf(`%s Generated aggregate: %s

Next steps:
  1. Implement your domain logic in %s
  2. Add command handlers in %s
  3. Create projections in %s`,
				styles.IconSuccess,
				data.Name,
				aggFile,
				cfg.Generation.CommandPackage,
				cfg.Generation.ProjectionPackage,
			)))

			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&events, "events", "e", nil, "Events to generate (comma-separated)")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")

	return cmd
}

func newGenerateEventCommand() *cobra.Command {
	var aggregate string
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:     "event <name>",
		Short:   "Generate an event",
		Aliases: []string{"evt", "e"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			if err := promptInput("Aggregate Name", "The aggregate this event belongs to",
				"Order", &aggregate, nonInteractive); err != nil {
				return err
			}

			eventsDir := cfg.Generation.EventPackage
			if err := os.MkdirAll(eventsDir, 0755); err != nil {
				return err
			}

			eventData := SingleEventData{
				Module:    cfg.Project.Module,
				Package:   filepath.Base(cfg.Generation.EventPackage),
				Name:      toPascalCase(name),
				Aggregate: toPascalCase(aggregate),
			}

			eventFile := filepath.Join(eventsDir, strings.ToLower(name)+".go")
			if err := generateFile(eventFile, singleEventTemplate, eventData); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", eventFile)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&aggregate, "aggregate", "a", "", "Aggregate this event belongs to")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")

	return cmd
}

func newGenerateProjectionCommand() *cobra.Command {
	var events []string
	var nonInteractive bool

	cmd := &cobra.Command{
		Use:     "projection <name>",
		Short:   "Generate a projection",
		Aliases: []string{"proj", "p"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			// Interactive event selection if none provided
			if len(events) == 0 {
				var eventsInput string
				if err := promptInput("Handled Events", "Comma-separated list of event types this projection handles",
					"OrderCreated,ItemAdded,OrderShipped", &eventsInput, nonInteractive); err != nil {
					return err
				}
				events = parseCommaSeparated(eventsInput)
			}

			projDir := cfg.Generation.ProjectionPackage
			if err := os.MkdirAll(projDir, 0755); err != nil {
				return err
			}

			projData := ProjectionData{
				Module:  cfg.Project.Module,
				Package: filepath.Base(cfg.Generation.ProjectionPackage),
				Name:    toPascalCase(name),
				Events:  events,
			}

			projFile := filepath.Join(projDir, strings.ToLower(name)+".go")
			if err := generateFile(projFile, projectionTemplate, projData); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", projFile)))

			testFile := filepath.Join(projDir, strings.ToLower(name)+"_test.go")
			if err := generateFile(testFile, projectionTestTemplate, projData); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", testFile)))
			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&events, "events", "e", nil, "Events this projection handles")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")

	return cmd
}

func newGenerateCommandCommand() *cobra.Command {
	var aggregate string
	var nonInteractive bool
	cmd := &cobra.Command{
		Use:     "command <name>",
		Short:   "Generate a command and handler",
		Aliases: []string{"cmd", "c"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, _, err := loadConfigOrDefault()
			if err != nil {
				return err
			}

			if err := promptInput("Aggregate Name", "The aggregate this command operates on",
				"Order", &aggregate, nonInteractive); err != nil {
				return err
			}

			cmdDir := cfg.Generation.CommandPackage
			if err := os.MkdirAll(cmdDir, 0755); err != nil {
				return err
			}

			cmdData := CommandData{
				Module:    cfg.Project.Module,
				Package:   filepath.Base(cfg.Generation.CommandPackage),
				Name:      toPascalCase(name),
				Aggregate: toPascalCase(aggregate),
			}

			cmdFile := filepath.Join(cmdDir, strings.ToLower(name)+".go")
			if err := generateFile(cmdFile, commandTemplate, cmdData); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", cmdFile)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&aggregate, "aggregate", "a", "", "Aggregate this command operates on")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")

	return cmd
}

// Helper functions and templates

type AggregateData struct {
	Name    string
	Module  string
	Package string
	Events  []EventData
}

type EventData struct {
	Name          string
	AggregateName string
}

type EventFileData struct {
	Module    string
	Package   string
	Aggregate string
	Events    []EventData
}

type SingleEventData struct {
	Module    string
	Package   string
	Name      string
	Aggregate string
}

type ProjectionData struct {
	Module  string
	Package string
	Name    string
	Events  []string
}

type CommandData struct {
	Module    string
	Package   string
	Name      string
	Aggregate string
}

func toPascalCase(s string) string {
	if s == "" {
		return s
	}
	result := make([]rune, 0, len(s))
	capitalizeNext := true
	for _, r := range s {
		if r == '_' || r == '-' || r == ' ' {
			capitalizeNext = true
			continue
		}
		if capitalizeNext {
			result = append(result, unicode.ToUpper(r))
			capitalizeNext = false
		} else {
			result = append(result, r)
		}
	}
	return string(result)
}

func generateFile(path string, tmpl string, data interface{}) error {
	funcMap := template.FuncMap{
		"ToLower": strings.ToLower,
	}
	t, err := template.New("file").Funcs(funcMap).Parse(tmpl)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}

	return os.WriteFile(path, buf.Bytes(), 0644)
}

var aggregateTemplate = `package {{.Package}}

import (
	"errors"

	"github.com/AshkanYarmoradi/go-mink"
)

// {{.Name}} represents the {{.Name}} aggregate.
type {{.Name}} struct {
	mink.AggregateBase
	
	// Add your aggregate state here
	// Example:
	// Status string
	// Items  []Item
}

// New{{.Name}} creates a new {{.Name}} aggregate.
func New{{.Name}}(id string) *{{.Name}} {
	agg := &{{.Name}}{}
	agg.SetID(id)
	agg.SetType("{{.Name}}")
	return agg
}

// ApplyEvent applies an event to the aggregate state.
func (a *{{.Name}}) ApplyEvent(event interface{}) error {
	switch e := event.(type) {
	{{- range .Events}}
	case {{.Name}}:
		return a.apply{{.Name}}(e)
	case *{{.Name}}:
		return a.apply{{.Name}}(*e)
	{{- end}}
	default:
		return errors.New("unknown event type")
	}
}

{{range .Events}}
func (a *{{$.Name}}) apply{{.Name}}(e {{.Name}}) error {
	// TODO: Apply the event to aggregate state
	return nil
}
{{end}}

// Domain methods - implement your business logic here
// Example:
// func (a *{{.Name}}) Create() error {
//     if a.Version() > 0 {
//         return errors.New("{{.Name | ToLower}} already exists")
//     }
//     a.Apply({{.Name}}Created{ID: a.AggregateID()})
//     return a.ApplyEvent({{.Name}}Created{ID: a.AggregateID()})
// }
`

var eventsFileTemplate = `package {{.Package}}

import "time"

{{range .Events}}
// {{.Name}} is emitted when {{$.Aggregate}} {{.Name | ToLower}}.
type {{.Name}} struct {
	{{$.Aggregate}}ID string    ` + "`json:\"{{$.Aggregate | ToLower}}_id\"`" + `
	Timestamp        time.Time ` + "`json:\"timestamp\"`" + `
	// Add event-specific fields here
}

// EventType returns the event type name.
func (e {{.Name}}) EventType() string {
	return "{{.Name}}"
}
{{end}}
`

var singleEventTemplate = `package {{.Package}}

import "time"

// {{.Name}} is emitted when {{.Aggregate}} {{.Name | ToLower}}.
type {{.Name}} struct {
	{{.Aggregate}}ID string    ` + "`json:\"{{.Aggregate | ToLower}}_id\"`" + `
	Timestamp        time.Time ` + "`json:\"timestamp\"`" + `
	// Add event-specific fields here
}

// EventType returns the event type name.
func (e {{.Name}}) EventType() string {
	return "{{.Name}}"
}
`

var aggregateTestTemplate = `package {{.Package}}

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew{{.Name}}(t *testing.T) {
	agg := New{{.Name}}("test-id")
	
	assert.Equal(t, "test-id", agg.AggregateID())
	assert.Equal(t, "{{.Name}}", agg.AggregateType())
	assert.Equal(t, int64(0), agg.Version())
}

// TODO: Add tests for your domain methods
// Example:
// func Test{{.Name}}_Create(t *testing.T) {
//     agg := New{{.Name}}("test-id")
//     
//     err := agg.Create()
//     
//     require.NoError(t, err)
//     events := agg.UncommittedEvents()
//     require.Len(t, events, 1)
//     
//     created, ok := events[0].({{.Name}}Created)
//     require.True(t, ok)
//     assert.Equal(t, "test-id", created.{{.Name}}ID)
// }
`

var projectionTemplate = `package {{.Package}}

import (
	"context"
	"encoding/json"

	"github.com/AshkanYarmoradi/go-mink"
)

// {{.Name}} is a read model projection.
type {{.Name}} struct {
	// Add your read model state here
	// This will be materialized from events
}

// {{.Name}}Projection handles events for the {{.Name}} read model.
type {{.Name}}Projection struct {
	// Add dependencies here (e.g., repository)
}

// New{{.Name}}Projection creates a new {{.Name}} projection.
func New{{.Name}}Projection() *{{.Name}}Projection {
	return &{{.Name}}Projection{}
}

// Name returns the projection name.
func (p *{{.Name}}Projection) Name() string {
	return "{{.Name}}"
}

// HandledEvents returns the event types this projection handles.
func (p *{{.Name}}Projection) HandledEvents() []string {
	return []string{
		{{- range .Events}}
		"{{.}}",
		{{- end}}
	}
}

// Apply applies an event to the projection.
func (p *{{.Name}}Projection) Apply(ctx context.Context, event mink.StoredEvent) error {
	switch event.Type {
	{{- range .Events}}
	case "{{.}}":
		return p.handle{{.}}(ctx, event)
	{{- end}}
	}
	return nil
}

{{range .Events}}
func (p *{{$.Name}}Projection) handle{{.}}(ctx context.Context, event mink.StoredEvent) error {
	var e struct {
		// Add event fields here
	}
	if err := json.Unmarshal(event.Data, &e); err != nil {
		return err
	}
	
	// TODO: Update read model
	return nil
}
{{end}}
`

var projectionTestTemplate = `package {{.Package}}

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew{{.Name}}Projection(t *testing.T) {
	proj := New{{.Name}}Projection()
	
	assert.Equal(t, "{{.Name}}", proj.Name())
	assert.NotEmpty(t, proj.HandledEvents())
}

// TODO: Add tests for event handlers
// Example:
// func Test{{.Name}}Projection_HandleEvent(t *testing.T) {
//     proj := New{{.Name}}Projection()
//     ctx := context.Background()
//     
//     event := mink.StoredEvent{
//         Type: "SomeEvent",
//         Data: []byte(` + "`{\"id\": \"123\"}`" + `),
//     }
//     
//     err := proj.Apply(ctx, event)
//     require.NoError(t, err)
// }
`

var commandTemplate = `package {{.Package}}

import (
	"context"
	"errors"

	"github.com/AshkanYarmoradi/go-mink"
)

// {{.Name}} is a command to {{.Name | ToLower}} on {{.Aggregate}}.
type {{.Name}} struct {
	{{.Aggregate}}ID string
	// Add command fields here
}

// AggregateID returns the target aggregate ID.
func (c {{.Name}}) AggregateID() string {
	return c.{{.Aggregate}}ID
}

// CommandType returns the command type name.
func (c {{.Name}}) CommandType() string {
	return "{{.Name}}"
}

// Validate validates the command.
func (c {{.Name}}) Validate() error {
	if c.{{.Aggregate}}ID == "" {
		return errors.New("{{.Aggregate | ToLower}}_id is required")
	}
	// Add validation logic here
	return nil
}

// {{.Name}}Handler handles {{.Name}} commands.
type {{.Name}}Handler struct {
	store *mink.EventStore
}

// New{{.Name}}Handler creates a new {{.Name}} handler.
func New{{.Name}}Handler(store *mink.EventStore) *{{.Name}}Handler {
	return &{{.Name}}Handler{store: store}
}

// Handle processes the {{.Name}} command.
func (h *{{.Name}}Handler) Handle(ctx context.Context, cmd {{.Name}}) error {
	// TODO: Implement command handling
	// 1. Load aggregate
	// 2. Execute domain logic
	// 3. Save aggregate
	return nil
}
`
