package commands

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	mink "go-mink.dev"
	"go-mink.dev/cli/styles"
	"go-mink.dev/cli/ui"
)

// NewGdprCommand creates the gdpr command group: subject-centric data-governance
// operations (discovery, erasure planning, readiness verification, retention
// preview) built on the store's subject-tagging + field-encryption metadata.
//
// The CLI operates against the event store through the diagnostic adapter and
// deliberately does NOT hold the application's encryption keys. So it performs the
// read-only half of GDPR workflows — resolving footprints and producing auditable
// plans/reports — while actual key revocation (crypto-shredding) is executed from
// the application via the DataEraser / RetentionManager APIs, which own the keys.
func NewGdprCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gdpr",
		Short: "GDPR data-governance operations (subject discovery, erasure, retention)",
		Long: `Subject-centric data-governance tooling over the event store.

Examples:
  mink gdpr discover user-123          # resolve a subject's complete footprint
  mink gdpr verify user-123            # erasure readiness (encrypted vs residual cleartext)
  mink gdpr erase user-123             # print the erasure plan (keys to revoke)
  mink gdpr retain --category Customer --max-age 8760h   # dry-run a retention policy

The CLI does not hold your encryption keys: discover/verify/erase/retain are
read-only reports and plans. Enforce erasure/retention from your application via
mink.NewDataEraser(...).Erase and mink.NewRetentionManager(...).Apply.`,
	}

	cmd.AddCommand(newGdprDiscoverCommand())
	cmd.AddCommand(newGdprVerifyCommand())
	cmd.AddCommand(newGdprEraseCommand())
	cmd.AddCommand(newGdprRetainCommand())

	return requireSubcommand(cmd)
}

// gdprStore wraps the diagnostic adapter in a read-only EventStore so the gdpr
// subcommands can drive the real subject-resolution and retention APIs. No
// encryption provider is configured — the CLI never revokes keys.
func gdprStore(ctx context.Context) (*mink.EventStore, func(), error) {
	adapter, cleanup, err := getAdapter(ctx)
	if err != nil {
		return nil, nil, err
	}
	return mink.New(adapter), cleanup, nil
}

// taggedForSubject reports whether an event's metadata tags the given subject.
func taggedForSubject(md mink.Metadata, subjectID string) bool {
	for _, s := range mink.GetSubjectTags(md) {
		if s == subjectID {
			return true
		}
	}
	return false
}

// terminalSafe makes a value that originates in the event store — a key id, a
// stream id, a subject id, or an error built from them — safe to print: every
// control character (newline, carriage return, tab, ESC and the rest of C0/C1)
// and the Unicode line/paragraph separators are replaced with U+FFFD, so a
// poisoned value can neither forge extra report lines nor hide or overwrite real
// ones through an ANSI escape sequence. Everything else is printed as-is.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 {
			return unicode.ReplacementChar
		}
		return r
	}, s)
}

// printFootprint renders a resolved SubjectFootprint (shared by discover and erase).
// Every stored value it prints goes through terminalSafe.
func printFootprint(fp *mink.SubjectFootprint) {
	fmt.Println()
	fmt.Println(styles.Title.Render(fmt.Sprintf("%s Subject footprint: %s", styles.IconDatabase, terminalSafe(fp.SubjectID))))
	fmt.Println()

	details := []string{
		fmt.Sprintf("Streams:         %d", len(fp.Streams)),
		fmt.Sprintf("Tagged events:   %d", fp.EventCount),
		fmt.Sprintf("Encryption keys: %d", len(fp.KeyIDs)),
	}
	for _, d := range details {
		fmt.Println("  " + styles.Normal.Render(d))
	}

	if fp.Partial {
		fmt.Println()
		fmt.Println(styles.FormatWarning("Footprint is PARTIAL — untagged (legacy) events exist that may belong to this subject; treat as incomplete"))
	}

	if len(fp.Streams) > 0 {
		fmt.Println()
		table := ui.NewTable("Stream", "Tagged events")
		for _, s := range fp.Streams {
			table.AddRow(terminalSafe(s), fmt.Sprintf("%d", fp.StreamEventCounts[s]))
		}
		fmt.Println(table.Render())
	}
}

func newGdprDiscoverCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "discover <subject-id>",
		Short: "Resolve a data subject's complete footprint (streams, events, keys)",
		Long: `Resolve every stream and event tagged for a data subject, plus the distinct
encryption keys protecting them. Read-only — this is the erasure preview.

Completeness depends on subject tagging (WithSubjectTagger) having been applied
uniformly; if legacy untagged events exist, the footprint is reported as PARTIAL.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subjectID := args[0]
			ctx := cmd.Context()

			store, cleanup, err := gdprStore(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			fp, err := mink.NewSubjectResolver(store).Resolve(ctx, subjectID)
			if err != nil {
				return err
			}

			printFootprint(fp)

			if len(fp.KeyIDs) > 0 {
				fmt.Println()
				fmt.Println(styles.Subtitle.Render(fmt.Sprintf("%s Encryption keys", styles.IconKey)))
				for _, k := range fp.KeyIDs {
					fmt.Println("  " + styles.IconDot + " " + terminalSafe(k))
				}
			}
			fmt.Println()
			return nil
		},
	}
}

func newGdprVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <subject-id>",
		Short: "Report a subject's erasure readiness (encrypted vs residual cleartext)",
		Long: `Classify a subject's tagged events by encryption posture:

  • encrypted events are crypto-shreddable — erased by revoking their key
  • cleartext events are RESIDUAL — written before field encryption was enabled,
    they cannot be crypto-shredded and must be remediated on the read side
    (a RedactFields / Anonymize retention policy)

Revocation-state verification (whether a key is already revoked) requires your
application's encryption provider; run it via the DataEraser.Verify API.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subjectID := args[0]
			ctx := cmd.Context()

			store, cleanup, err := gdprStore(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			fp, err := mink.NewSubjectResolver(store).Resolve(ctx, subjectID)
			if err != nil {
				return err
			}

			var encrypted, cleartext int
			for _, streamID := range fp.Streams {
				events, err := store.LoadRaw(ctx, streamID, 0)
				if err != nil {
					return err
				}
				for _, se := range events {
					if !taggedForSubject(se.Metadata, subjectID) {
						continue
					}
					if mink.IsEncrypted(se.Metadata) {
						encrypted++
					} else {
						cleartext++
					}
				}
			}

			fmt.Println()
			fmt.Println(styles.Title.Render(fmt.Sprintf("%s Erasure readiness: %s", styles.IconLock, terminalSafe(subjectID))))
			fmt.Println()
			details := []string{
				fmt.Sprintf("Tagged events:          %d", fp.EventCount),
				fmt.Sprintf("Encrypted (shreddable): %d", encrypted),
				fmt.Sprintf("Cleartext (residual):   %d", cleartext),
				fmt.Sprintf("Encryption keys:        %d", len(fp.KeyIDs)),
			}
			for _, d := range details {
				fmt.Println("  " + styles.Normal.Render(d))
			}
			fmt.Println()

			switch {
			case cleartext > 0:
				fmt.Println(styles.FormatWarning(fmt.Sprintf("%d cleartext event(s) cannot be crypto-shredded — remediate via a RedactFields/Anonymize retention policy", cleartext)))
			case fp.Partial:
				fmt.Println(styles.FormatWarning("Footprint is PARTIAL — untagged events exist; readiness is a lower bound"))
			default:
				fmt.Println(styles.FormatSuccess("All tagged events are encrypted — the subject is fully crypto-shreddable"))
			}
			if cleartext > 0 && fp.Partial {
				fmt.Println(styles.FormatWarning("Footprint is PARTIAL — untagged events exist; readiness is a lower bound"))
			}
			fmt.Println()
			return nil
		},
	}
}

func newGdprEraseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "erase <subject-id>",
		Short: "Print the erasure plan for a subject (encryption keys to revoke)",
		Long: `Resolve a subject's footprint and list the encryption keys that must be revoked
to crypto-shred them.

The CLI does not hold your application's encryption keys, so it does NOT perform
revocation. Execute the erasure from your application via the DataEraser API
(mink.NewDataEraser(...).Erase), or revoke the listed keys directly in your KMS /
Vault. This command produces the auditable erasure plan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subjectID := args[0]
			ctx := cmd.Context()

			store, cleanup, err := gdprStore(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			fp, err := mink.NewSubjectResolver(store).Resolve(ctx, subjectID)
			if err != nil {
				return err
			}

			printFootprint(fp)
			fmt.Println()

			if len(fp.KeyIDs) == 0 {
				fmt.Println(styles.FormatInfo("No encryption keys found for this subject — nothing to crypto-shred (any cleartext events must be handled on the read side)"))
				fmt.Println()
				return nil
			}

			fmt.Println(styles.Subtitle.Render(fmt.Sprintf("%s Erasure plan — revoke these keys", styles.IconKey)))
			fmt.Println()
			for _, k := range fp.KeyIDs {
				fmt.Println("  " + styles.IconArrow + " " + terminalSafe(k))
			}
			fmt.Println()
			fmt.Println(styles.FormatInfo("Execute via mink.NewDataEraser(store, ...).Erase — the CLI does not hold your keys, so it will not revoke them here"))
			fmt.Println()
			return nil
		},
	}
}

// gdprStoreOpener opens the read-only EventStore a gdpr subcommand works on. The
// production opener is gdprStore (the diagnostic adapter named by mink.yaml); tests
// inject a pre-seeded in-memory store.
type gdprStoreOpener func(ctx context.Context) (*mink.EventStore, func(), error)

func newGdprRetainCommand() *cobra.Command {
	return newGdprRetainCommandWithStore(gdprStore)
}

// newGdprRetainCommandWithStore builds the retain command over the given store opener.
func newGdprRetainCommandWithStore(open gdprStoreOpener) *cobra.Command {
	var (
		prefix    string
		category  string
		tenant    string
		eventType string
		maxAge    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "retain",
		Short: "Preview (dry-run) which events a retention policy would crypto-shred",
		Long: `Scan the store and report how many events a retention policy matches, without
making any change — together with the blast radius a shred sweep would have: the
encryption keys it would revoke, the shared keys the blast-radius guard refuses to
revoke because they also protect events outside the policy, and the matches that
carry no encryption envelope and therefore cannot be crypto-shredded. Actual
enforcement (key revocation) requires your application's encryption provider; run
it via mink.NewRetentionManager(...).Apply.

At least one matcher (--prefix, --category, --tenant, --event-type, or --max-age)
is required.

Examples:
  mink gdpr retain --category Customer --max-age 8760h   # customers older than 1y
  mink gdpr retain --prefix order- --event-type OrderPlaced`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if prefix == "" && category == "" && tenant == "" && eventType == "" && maxAge == 0 {
				return fmt.Errorf("at least one matcher is required (--prefix, --category, --tenant, --event-type, or --max-age)")
			}

			store, cleanup, err := open(ctx)
			if err != nil {
				return err
			}
			defer cleanup()

			policy := mink.RetentionPolicy{
				Name:         "cli-preview",
				StreamPrefix: prefix,
				Category:     category,
				TenantID:     tenant,
				MaxAge:       maxAge,
				Action:       mink.ActionShred,
			}
			if eventType != "" {
				policy.EventTypes = []string{eventType}
			}

			report, err := mink.NewRetentionManager(store, []mink.RetentionPolicy{policy}).DryRun(ctx)
			if err != nil {
				return err
			}

			printRetentionReport(report)
			return nil
		},
	}

	cmd.Flags().StringVar(&prefix, "prefix", "", "Match stream-id prefix")
	cmd.Flags().StringVar(&category, "category", "", "Match stream category (text before the first '-')")
	cmd.Flags().StringVar(&tenant, "tenant", "", "Match metadata tenant id")
	cmd.Flags().StringVar(&eventType, "event-type", "", "Match event type")
	cmd.Flags().DurationVar(&maxAge, "max-age", 0, "Match events older than this age (e.g. 8760h)")

	return cmd
}

// printRetentionReport renders a RetentionReport for `gdpr retain`. Beyond the
// Scanned/Matched counts it shows the blast radius the shred guard computed — the keys a
// sweep would revoke, the shared keys it refuses, how many matches are plaintext, what
// (if anything) was revoked, and the errors — as key ids and counts only; event payloads
// and stream ids never reach the output.
func printRetentionReport(report *mink.RetentionReport) {
	title := " Retention sweep"
	if report.DryRun {
		title = " Retention preview (dry-run)"
	}
	fmt.Println()
	fmt.Println(styles.Title.Render(styles.IconChart + title))
	fmt.Println()

	revoked := countWithKeyIDs("Keys revoked:        ", report.KeysRevoked)
	if report.DryRun {
		revoked += " (dry-run: nothing is revoked)"
	}
	details := []string{
		fmt.Sprintf("Scanned:  %d events", report.Scanned),
		fmt.Sprintf("Matched:  %d events", report.Matched),
		fmt.Sprintf("Unencrypted matches: %d (plaintext — cannot be crypto-shredded)", report.UnencryptedMatches),
		countWithKeyIDs("Keys to revoke:      ", report.KeysToRevoke),
		countWithKeyIDs("Shared keys skipped: ", report.SharedKeysSkipped),
		revoked,
		fmt.Sprintf("Errors:              %d", len(report.Errors)),
	}
	for _, d := range details {
		fmt.Println("  " + styles.Normal.Render(d))
	}
	fmt.Println()

	switch {
	case report.Matched == 0:
		fmt.Println(styles.FormatInfo("No events match this policy"))
	case report.DryRun:
		fmt.Println(styles.FormatWarning(fmt.Sprintf("%d event(s) would be crypto-shredded — run RetentionManager.Apply with your encryption provider to enforce", report.Matched)))
	default:
		fmt.Println(styles.FormatInfo(fmt.Sprintf("%d event(s) matched — see Keys revoked for what was crypto-shredded", report.Matched)))
	}
	if n := len(report.SharedKeysSkipped); n > 0 {
		fmt.Println(styles.FormatWarning(fmt.Sprintf("%d key(s) refused by the shared-key guard — they also protect events outside this policy and will NOT be revoked (use per-scope keys, or WithAllowSharedKeyRevocation to accept the blast radius)", n)))
	}
	if n := report.UnencryptedMatches; n > 0 {
		fmt.Println(styles.FormatWarning(fmt.Sprintf("%d matched event(s) carry no encryption envelope and would remain in plaintext — remediate via a RedactFields/Anonymize policy", n)))
	}
	if len(report.Errors) > 0 {
		fmt.Println()
		fmt.Println(styles.Subtitle.Render(styles.IconWarning + " Errors"))
		for _, e := range report.Errors {
			fmt.Println("  " + styles.IconDot + " " + terminalSafe(e.Error()))
		}
	}
	fmt.Println()
}

// countWithKeyIDs renders "<label><count> [id, id]", listing the ids only when there are
// any so an empty line never carries a dangling bracket.
// The ids are stored values and are sanitized with terminalSafe before printing.
func countWithKeyIDs(label string, ids []string) string {
	s := fmt.Sprintf("%s%d", label, len(ids))
	if len(ids) > 0 {
		safe := make([]string, len(ids))
		for i, id := range ids {
			safe[i] = terminalSafe(id)
		}
		s += " [" + strings.Join(safe, ", ") + "]"
	}
	return s
}
