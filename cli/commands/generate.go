package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"go-mink.dev/cli/config"
	"go-mink.dev/cli/styles"
)

// promptInput runs an interactive input form and returns the entered value.
// It only prompts if nonInteractive is false and the current value is empty.
//
// In non-interactive mode a prompt cannot be shown, so if the value is still
// empty the caller must have supplied it via flags. requiredFlag names the flag
// the user should set; when it is non-empty and the value is missing,
// promptInput returns an error instead of silently leaving the value blank
// (which previously produced malformed scaffolding). Pass an empty requiredFlag
// for genuinely optional inputs, where an empty value is acceptable.
func promptInput(title, description, placeholder string, value *string, nonInteractive bool, requiredFlag string) error {
	if *value != "" {
		return nil
	}
	if nonInteractive {
		if requiredFlag != "" {
			return fmt.Errorf("%s is required in non-interactive mode", requiredFlag)
		}
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

	return requireSubcommand(cmd)
}

func newGenerateAggregateCommand() *cobra.Command {
	var events []string
	var nonInteractive bool
	var force bool

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
			cfg, root, _, err := loadConfigOrDefaultWithRoot()
			if err != nil {
				return err
			}

			// Interactive event selection if none provided
			if len(events) == 0 {
				var eventsInput string
				if err := promptInput("Events", "Comma-separated list of events (e.g., Created,Updated,Deleted)",
					"Created,Updated,Deleted", &eventsInput, nonInteractive, ""); err != nil {
					return err
				}
				events = parseCommaSeparated(eventsInput)
			}

			// Validate everything that ends up in generated Go source or in a
			// file location before touching the filesystem: mink.yaml is not
			// trusted input.
			aggDir := cfg.Generation.AggregatePackage
			if err := checkOutputDir(root, "aggregate_package", aggDir); err != nil {
				return err
			}
			aggPkg, err := packageNameFromPath("aggregate_package", aggDir)
			if err != nil {
				return err
			}
			aggName := toPascalCase(name)
			if err := validateIdentifier("aggregate", aggName); err != nil {
				return err
			}

			data := AggregateData{
				Name:    aggName,
				Module:  cfg.Project.Module,
				Package: aggPkg,
				Events:  make([]EventData, 0, len(events)),
			}
			for _, e := range events {
				eventName := toPascalCase(e)
				if err := validateIdentifier("event", eventName); err != nil {
					return err
				}
				data.Events = append(data.Events, EventData{
					Name:          eventName,
					AggregateName: data.Name,
				})
			}

			var eventsDir, eventPkg string
			if len(events) > 0 {
				eventsDir = cfg.Generation.EventPackage
				if err := checkOutputDir(root, "event_package", eventsDir); err != nil {
					return err
				}
				eventPkg, err = packageNameFromPath("event_package", eventsDir)
				if err != nil {
					return err
				}
			}

			// Create aggregate file
			if err := os.MkdirAll(aggDir, 0755); err != nil {
				return err
			}

			aggFile := filepath.Join(aggDir, strings.ToLower(name)+".go")
			if err := generateFile(aggFile, aggregateTemplate, data, force); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", aggFile)))

			// Create events file if events provided
			if len(events) > 0 {
				if err := os.MkdirAll(eventsDir, 0755); err != nil {
					return err
				}

				eventsFile := filepath.Join(eventsDir, strings.ToLower(name)+"_events.go")
				eventFileData := EventFileData{
					Module:    cfg.Project.Module,
					Package:   eventPkg,
					Aggregate: data.Name,
					Events:    data.Events,
				}
				if err := generateFile(eventsFile, eventsFileTemplate, eventFileData, force); err != nil {
					return err
				}
				fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", eventsFile)))
			}

			// Create test file
			testFile := filepath.Join(aggDir, strings.ToLower(name)+"_test.go")
			if err := generateFile(testFile, aggregateTestTemplate, data, force); err != nil {
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
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")

	return cmd
}

// generateWithAggregate is a helper that creates generator commands that need an aggregate reference.
type generateWithAggregateParams struct {
	use     string
	short   string
	aliases []string
	// kind names the generated artifact ("event", "command") in error messages.
	kind           string
	aggregateLabel string
	// packageSetting is the mink.yaml generation.* key that supplies the
	// output directory; it is named in validation errors.
	packageSetting string
	getOutputDir   func(*config.Config) string
	template       string
	// makeData builds the template data from already-validated inputs: pkg is
	// the package identifier, name and aggregate are PascalCase identifiers.
	makeData func(cfg *config.Config, pkg, name, aggregate string) interface{}
}

func newGenerateWithAggregateCommand(params generateWithAggregateParams) *cobra.Command {
	var aggregate string
	var nonInteractive bool
	var force bool

	cmd := &cobra.Command{
		Use:     params.use,
		Short:   params.short,
		Aliases: params.aliases,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, root, _, err := loadConfigOrDefaultWithRoot()
			if err != nil {
				return err
			}

			if err := promptInput("Aggregate Name", params.aggregateLabel,
				"Order", &aggregate, nonInteractive, "--aggregate"); err != nil {
				return err
			}

			// Validate the identifiers spliced into the template and the
			// output location (from mink.yaml) before writing anything.
			typeName := toPascalCase(name)
			if err := validateIdentifier(params.kind, typeName); err != nil {
				return err
			}
			aggregateName := toPascalCase(aggregate)
			if err := validateIdentifier("aggregate", aggregateName); err != nil {
				return err
			}

			outputDir := params.getOutputDir(cfg)
			if err := checkOutputDir(root, params.packageSetting, outputDir); err != nil {
				return err
			}
			pkg, err := packageNameFromPath(params.packageSetting, outputDir)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				return err
			}

			data := params.makeData(cfg, pkg, typeName, aggregateName)
			outputFile := filepath.Join(outputDir, strings.ToLower(name)+".go")
			if err := generateFile(outputFile, params.template, data, force); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", outputFile)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&aggregate, "aggregate", "a", "", "Aggregate this belongs to")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")

	return cmd
}

func newGenerateEventCommand() *cobra.Command {
	return newGenerateWithAggregateCommand(generateWithAggregateParams{
		use:            "event <name>",
		short:          "Generate an event",
		aliases:        []string{"evt", "e"},
		kind:           "event",
		aggregateLabel: "The aggregate this event belongs to",
		packageSetting: "event_package",
		getOutputDir:   func(cfg *config.Config) string { return cfg.Generation.EventPackage },
		template:       singleEventTemplate,
		makeData: func(cfg *config.Config, pkg, name, aggregate string) interface{} {
			return SingleEventData{
				Module:    cfg.Project.Module,
				Package:   pkg,
				Name:      name,
				Aggregate: aggregate,
			}
		},
	})
}

func newGenerateProjectionCommand() *cobra.Command {
	var events []string
	var nonInteractive bool
	var force bool

	cmd := &cobra.Command{
		Use:     "projection <name>",
		Short:   "Generate a projection",
		Aliases: []string{"proj", "p"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, root, _, err := loadConfigOrDefaultWithRoot()
			if err != nil {
				return err
			}

			// Interactive event selection if none provided
			if len(events) == 0 {
				var eventsInput string
				if err := promptInput("Handled Events", "Comma-separated list of event types this projection handles",
					"OrderCreated,ItemAdded,OrderShipped", &eventsInput, nonInteractive, ""); err != nil {
					return err
				}
				events = parseCommaSeparated(eventsInput)
			}

			projName := toPascalCase(name)
			if err := validateIdentifier("projection", projName); err != nil {
				return err
			}
			// Handled event types are spliced into method names and string
			// literals. Each entry is PascalCased first — exactly as the aggregate
			// generator treats its --events, so kebab/snake-case inputs such as
			// order-created are accepted and yield the Go type name OrderCreated
			// that a registered event type carries — and must then be a plain
			// identifier.
			handled := make([]string, 0, len(events))
			for _, e := range events {
				eventName := toPascalCase(e)
				if err := validateIdentifier("event", eventName); err != nil {
					return err
				}
				handled = append(handled, eventName)
			}

			projDir := cfg.Generation.ProjectionPackage
			if err := checkOutputDir(root, "projection_package", projDir); err != nil {
				return err
			}
			projPkg, err := packageNameFromPath("projection_package", projDir)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(projDir, 0755); err != nil {
				return err
			}

			projData := ProjectionData{
				Module:  cfg.Project.Module,
				Package: projPkg,
				Name:    projName,
				Events:  handled,
			}

			projFile := filepath.Join(projDir, strings.ToLower(name)+".go")
			if err := generateFile(projFile, projectionTemplate, projData, force); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", projFile)))

			testFile := filepath.Join(projDir, strings.ToLower(name)+"_test.go")
			if err := generateFile(testFile, projectionTestTemplate, projData, force); err != nil {
				return err
			}
			fmt.Println(styles.FormatSuccess(fmt.Sprintf("Created %s", testFile)))
			return nil
		},
	}

	cmd.Flags().StringSliceVarP(&events, "events", "e", nil, "Events this projection handles")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip interactive prompts (for scripting)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")

	return cmd
}

func newGenerateCommandCommand() *cobra.Command {
	return newGenerateWithAggregateCommand(generateWithAggregateParams{
		use:            "command <name>",
		short:          "Generate a command and handler",
		aliases:        []string{"cmd", "c"},
		kind:           "command",
		aggregateLabel: "The aggregate this command operates on",
		packageSetting: "command_package",
		getOutputDir:   func(cfg *config.Config) string { return cfg.Generation.CommandPackage },
		template:       commandTemplate,
		makeData: func(cfg *config.Config, pkg, name, aggregate string) interface{} {
			return CommandData{
				Module:    cfg.Project.Module,
				Package:   pkg,
				Name:      name,
				Aggregate: aggregate,
			}
		},
	})
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

var (
	// goPackageNameRe matches the identifiers accepted in the package clause
	// of generated files.
	goPackageNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// goIdentifierRe matches the identifiers accepted for generated type,
	// method and function names.
	goIdentifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// packageNameFromPath derives the Go package identifier for a configured
// output package path (its last path element) and validates it, so that a
// hostile or mistyped mink.yaml cannot inject arbitrary text into the package
// clause of generated files. setting names the mink.yaml key in the error.
func packageNameFromPath(setting, pkgPath string) (string, error) {
	name := filepath.Base(pkgPath)
	if !goPackageNameRe.MatchString(name) {
		return "", fmt.Errorf("generation.%s: %q does not end in a valid Go package name (last path element must match %s)",
			setting, pkgPath, goPackageNameRe)
	}
	return name, nil
}

// validateIdentifier checks that name, as it will appear in generated Go
// source, is a plain identifier. kind names the artifact in the error.
func validateIdentifier(kind, name string) error {
	if !goIdentifierRe.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must match %s", kind, name, goIdentifierRe)
	}
	return nil
}

// checkOutputDir verifies that a configured output directory stays inside
// root (the directory holding mink.yaml, or the working directory when the
// defaults are in use), so a hostile mink.yaml cannot direct generated files
// outside the project. setting names the mink.yaml key in the error.
func checkOutputDir(root, setting, dir string) error {
	if err := ensureInsideDir(root, dir); err != nil {
		return fmt.Errorf("generation.%s: %w", setting, err)
	}
	return nil
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

func generateFile(path string, tmpl string, data interface{}, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file %q (use --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %q: %w", path, err)
		}
	}
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

	"go-mink.dev"
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

	"go-mink.dev"
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

	"go-mink.dev"
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
