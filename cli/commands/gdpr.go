package commands

import (
	"context"
	"fmt"
	"time"

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

// printFootprint renders a resolved SubjectFootprint (shared by discover and erase).
func printFootprint(fp *mink.SubjectFootprint) {
	fmt.Println()
	fmt.Println(styles.Title.Render(fmt.Sprintf("%s Subject footprint: %s", styles.IconDatabase, fp.SubjectID)))
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
			table.AddRow(s, fmt.Sprintf("%d", fp.StreamEventCounts[s]))
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
					fmt.Println("  " + styles.IconDot + " " + k)
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
			fmt.Println(styles.Title.Render(fmt.Sprintf("%s Erasure readiness: %s", styles.IconLock, subjectID)))
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
				fmt.Println("  " + styles.IconArrow + " " + k)
			}
			fmt.Println()
			fmt.Println(styles.FormatInfo("Execute via mink.NewDataEraser(store, ...).Erase — the CLI does not hold your keys, so it will not revoke them here"))
			fmt.Println()
			return nil
		},
	}
}

func newGdprRetainCommand() *cobra.Command {
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
making any change. Actual enforcement (key revocation) requires your application's
encryption provider; run it via mink.NewRetentionManager(...).Apply.

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

			store, cleanup, err := gdprStore(ctx)
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

			fmt.Println()
			fmt.Println(styles.Title.Render(styles.IconChart + " Retention preview (dry-run)"))
			fmt.Println()
			details := []string{
				fmt.Sprintf("Scanned:  %d events", report.Scanned),
				fmt.Sprintf("Matched:  %d events", report.Matched),
			}
			for _, d := range details {
				fmt.Println("  " + styles.Normal.Render(d))
			}
			fmt.Println()
			if report.Matched == 0 {
				fmt.Println(styles.FormatInfo("No events match this policy"))
			} else {
				fmt.Println(styles.FormatWarning(fmt.Sprintf("%d event(s) would be crypto-shredded — run RetentionManager.Apply with your encryption provider to enforce", report.Matched)))
			}
			fmt.Println()
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
