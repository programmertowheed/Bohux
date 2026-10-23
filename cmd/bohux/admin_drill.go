package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/bohumix/bohux/internal/drill"
	"github.com/bohumix/bohux/internal/secrets/openbao"
)

// adminDrill proves one archive restores (ADR-0051). It is the hand-run half of the
// weekly schedule: the same engine, the same checks, on an archive the operator names.
func adminDrill(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	fs := newFlagSet("admin drill")
	in := fs.String("in", "", "archive to prove (required)")
	identity := fs.String("identity", getenv(envDrillIdentity),
		"age identity file that decrypts the archive (required, env "+envDrillIdentity+")")
	scratchDB := fs.String("drill-database-url", getenv(envDrillDBURL),
		"Postgres DSN of the CREATEDB drill role, which owns and migrates the scratch database (env "+envDrillDBURL+")")
	adminDB := fs.String("drill-admin-database-url", getenv(envDrillAdminDBURL),
		"Postgres DSN of the BYPASSRLS drill role, which loads the rows (env "+envDrillAdminDBURL+")")
	baoAddr := fs.String("openbao-address", getenv(envOpenBaoAddr), "OpenBao URL (env "+envOpenBaoAddr+")")
	baoToken := fs.String("openbao-token-file", getenv(envOpenBaoToken),
		"file holding the OpenBao token (env "+envOpenBaoToken+")")
	drillMount := fs.String("drill-kv-mount", getenv(envDrillKVMount),
		"OpenBao KV v2 mount the drill restores secrets into, and wipes (env "+envDrillKVMount+")")
	liveMount := fs.String("openbao-kv-mount", envOr(getenv, envOpenBaoMount, defaultOpenBaoMount),
		"the live KV v2 mount, which the drill refuses to touch (env "+envOpenBaoMount+")")
	verdictJSON := fs.Bool("json", false, "print the verdict as JSON")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("admin drill: %w", err)
	}
	if *in == "" || *identity == "" || *scratchDB == "" || *adminDB == "" {
		return errors.New("admin drill: --in, --identity, --drill-database-url, and --drill-admin-database-url are required")
	}
	// Checked with the other configuration, before anything is opened: a drill wipes
	// the mount it is given, so being told the wrong one is the costliest mistake here.
	if *baoAddr != "" && *drillMount == "" {
		return errors.New("admin drill: --drill-kv-mount is required with --openbao-address: a drill wipes the mount it is given")
	}
	idf, err := os.Open(*identity)
	if err != nil {
		return fmt.Errorf("admin drill: %w", err)
	}
	ids, err := age.ParseIdentities(idf)
	_ = idf.Close()
	if err != nil {
		return fmt.Errorf("admin drill: --identity: %w", err)
	}
	cfg := drill.Config{
		Archive: *in, Identities: ids,
		ScratchDSN: *scratchDB, AdminDSN: *adminDB, LiveMount: *liveMount,
	}
	if *baoAddr != "" {
		raw, err := os.ReadFile(*baoToken)
		if err != nil {
			return fmt.Errorf("admin drill: read OpenBao token file: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return errors.New("admin drill: OpenBao token file is empty")
		}
		cfg.Bao = &openbao.Client{Addr: *baoAddr, Token: token, Mount: *drillMount}
	}

	v, runErr := drill.Run(ctx, cfg)
	if v == nil {
		return fmt.Errorf("admin drill: %w", runErr)
	}
	if err := printVerdict(stdout, v, *verdictJSON); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("admin drill: %w", runErr)
	}
	return nil
}

// printVerdict writes the verdict for a person, or as the JSON the schedule records.
func printVerdict(stdout io.Writer, v *drill.Verdict, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("admin drill: write the verdict: %w", err)
		}
		return nil
	}
	outcome := "PASSED"
	if !v.OK() {
		outcome = "FAILED"
	}
	if _, err := fmt.Fprintf(stdout, "Restore drill %s: %s\n", outcome, v.Archive); err != nil {
		return err
	}
	for _, c := range v.Checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
		}
		if _, err := fmt.Fprintf(stdout, "  [%s] %-12s %s\n", mark, c.Name, c.Detail); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout,
		"Archive written %s by Bohux %s. Restored %d rows across %d tables, %d live tenant(s), "+
			"%d owner token(s), %d secret(s), in %s.\n",
		v.CreatedAt.Format("2006-01-02 15:04 MST"), v.BohuxVersion, v.Rows, v.Tables,
		v.Tenants, v.OwnerTokens, v.Secrets, v.FinishedAt.Sub(v.StartedAt).Round(time.Second))
	return err
}
