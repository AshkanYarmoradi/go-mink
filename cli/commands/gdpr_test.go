package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mink "go-mink.dev"
)

func TestNewGdprCommand(t *testing.T) {
	cmd := NewGdprCommand()

	assert.Equal(t, "gdpr", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)

	names := getSubcommandNames(cmd)
	for _, sub := range []string{"discover", "verify", "erase", "retain"} {
		assert.True(t, names[sub], "missing subcommand: %s", sub)
	}
}

func TestGdprCommand_RegisteredOnRoot(t *testing.T) {
	names := getSubcommandNames(NewRootCommand())
	assert.True(t, names["gdpr"], "gdpr command should be registered on the root command")
}

func TestGdprSubcommand_Structure(t *testing.T) {
	tests := []struct {
		subName string
		useStr  string
	}{
		{"discover", "discover <subject-id>"},
		{"verify", "verify <subject-id>"},
		{"erase", "erase <subject-id>"},
		{"retain", "retain"},
	}

	cmd := NewGdprCommand()
	for _, tt := range tests {
		t.Run(tt.subName, func(t *testing.T) {
			sub, _, err := cmd.Find([]string{tt.subName})
			require.NoError(t, err)
			assert.Equal(t, tt.useStr, sub.Use)
			assert.NotEmpty(t, sub.Short)
			assert.NotEmpty(t, sub.Long)
		})
	}
}

func TestGdprRetainCommand_Flags(t *testing.T) {
	cmd := NewGdprCommand()
	retain, _, err := cmd.Find([]string{"retain"})
	require.NoError(t, err)

	for _, flag := range []string{"prefix", "category", "tenant", "event-type", "max-age"} {
		assert.NotNil(t, retain.Flags().Lookup(flag), "flag %s should exist on retain", flag)
	}
}

func TestTaggedForSubject(t *testing.T) {
	tagged := mink.Metadata{Custom: map[string]string{mink.SubjectTagsKey: `["user-1","user-2"]`}}

	tests := []struct {
		name    string
		md      mink.Metadata
		subject string
		want    bool
	}{
		{"match first", tagged, "user-1", true},
		{"match second", tagged, "user-2", true},
		{"no match", tagged, "user-3", false},
		{"no tags", mink.Metadata{}, "user-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, taggedForSubject(tt.md, tt.subject))
		})
	}
}

// TestGdprCommand_Execute_MemoryDriver runs each read-only verb against a fresh,
// empty in-memory store — they must all succeed (empty footprint, nothing to do).
func TestGdprCommand_Execute_MemoryDriver(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"discover", []string{"discover", "user-123"}},
		{"verify", []string{"verify", "user-123"}},
		{"erase", []string{"erase", "user-123"}},
		{"retain by category", []string{"retain", "--category", "Customer"}},
		{"retain by max-age", []string{"retain", "--max-age", "1h"}},
		{"retain by prefix + event-type", []string{"retain", "--prefix", "order-", "--event-type", "OrderPlaced"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setupTestEnv(t, "mink-gdpr-*")
			env.createConfig(withDriver("memory"))

			cmd := NewGdprCommand()
			err := executeCmd(cmd, tt.args)
			assert.NoError(t, err)
		})
	}
}

func TestGdprRetainCommand_RequiresMatcher(t *testing.T) {
	env := setupTestEnv(t, "mink-gdpr-retain-*")
	env.createConfig(withDriver("memory"))

	cmd := NewGdprCommand()
	err := executeCmd(cmd, []string{"retain"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matcher")
}

func TestGdprDiscoverCommand_RequiresSubjectArg(t *testing.T) {
	cmd := NewGdprCommand()
	err := executeCmd(cmd, []string{"discover"})
	assert.Error(t, err) // cobra ExactArgs(1)
}

func TestGdprCommand_NoConfig(t *testing.T) {
	env := setupTestEnv(t, "mink-gdpr-noconfig-*")
	_ = env // intentionally no mink.yaml

	cmd := NewGdprCommand()
	err := executeCmd(cmd, []string{"discover", "user-123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mink.yaml")
}
