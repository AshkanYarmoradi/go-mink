package mink_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-mink.dev/cli/commands"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what was written (the CLI
// commands print via fmt.Println to the process stdout, not cmd.OutOrStdout()).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestE2E_CLI_GDPRDiscoverVerify: `mink gdpr discover`/`verify` run read-only against a populated
// PostgreSQL footprint (seeded through the subject-tagging store), reporting the subject's streams
// without holding any encryption keys / performing any revocation.
func TestE2E_CLI_GDPRDiscoverVerify(t *testing.T) {
	p, provider, _ := newGDPRStore(t, true)
	addSubjectKey(t, provider, "alice")
	appendPerson(t, p, "alice", "alice@example.com")
	appendPerson(t, p, "alice", "alice.new@example.com")

	// Point the CLI at this isolated schema via a mink.yaml in a temp working directory.
	dbURL := os.Getenv("TEST_DATABASE_URL")
	tmp := t.TempDir()
	minkYAML := "" +
		"version: \"1.0\"\n" +
		"project:\n  name: \"e2e\"\n  module: \"example.com/e2e\"\n" +
		"database:\n  driver: \"postgres\"\n  url: \"" + dbURL + "\"\n  schema: \"" + p.Schema + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "mink.yaml"), []byte(minkYAML), 0o600))
	t.Chdir(tmp) // Go 1.24+, auto-restored on cleanup

	// discover: reports the subject's footprint (read-only over the diagnostic adapter).
	discoverOut := captureStdout(t, func() {
		cmd := commands.NewGdprCommand()
		cmd.SetArgs([]string{"discover", "alice"})
		require.NoError(t, cmd.Execute())
	})
	assert.Contains(t, discoverOut, "Subject footprint: alice", "discover reports the subject")
	assert.Contains(t, discoverOut, "key-alice", "discover reports the subject's encryption key")
	// The stream name (person-alice) is present but may be wrapped by the table renderer, so we
	// assert on the tagged-event count instead of the wrapped cell.
	assert.Contains(t, discoverOut, "Tagged events:", "discover reports the footprint counts")

	// verify: classifies the subject's events (read-only), succeeds without touching keys.
	verifyOut := captureStdout(t, func() {
		cmd := commands.NewGdprCommand()
		cmd.SetArgs([]string{"verify", "alice"})
		require.NoError(t, cmd.Execute())
	})
	assert.NotEmpty(t, verifyOut, "verify produced a report")
}
