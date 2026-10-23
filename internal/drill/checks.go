package drill

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/secrets/openbao"
)

// inspect checks that what restore loaded is an install someone could actually run:
// the tables the manifest promised, a tenant, a way back in, a node CA the agents
// would still trust, and secret values at the versions Postgres records for them
// (ADR-0051 item 4). A failed check is recorded, not returned: the drill reports every
// finding rather than stopping at the first.
func inspect(ctx context.Context, cfg Config, v *Verdict, m *backup.Manifest, adminDSN, dataDir string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("drill: connect to the restored database: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	tables, err := tablesIn(ctx, conn)
	if err != nil {
		return fmt.Errorf("drill: %w", err)
	}
	if gone := missing(m.Tables, tables); len(gone) > 0 {
		v.add("tables", false, "the manifest names %d tables; %d are missing: %s",
			len(m.Tables), len(gone), quoteList(gone))
	} else {
		v.add("tables", true, "all %d tables the manifest names are present", len(m.Tables))
	}

	per, total, err := countRows(ctx, conn, tables)
	if err != nil {
		return fmt.Errorf("drill: %w", err)
	}
	v.PerTable, v.Rows = per, total

	checkTenants(ctx, conn, v)
	checkOwnerToken(ctx, conn, v)
	checkNodeCA(cfg, v, dataDir)
	checkSecrets(ctx, conn, cfg, v)
	return nil
}

// checkTenants fails an install with nothing in it: every archive of a working Bohux
// has at least the tenant the installer seeded.
func checkTenants(ctx context.Context, conn *pgx.Conn, v *Verdict) {
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM tenants_heads WHERE NOT deleted`).Scan(&n); err != nil {
		v.add("tenants", false, "count tenants: %v", err)
		return
	}
	v.Tenants = n
	if n == 0 {
		v.add("tenants", false, "the restored database holds no live tenant")
		return
	}
	v.add("tenants", true, "%d live tenant(s)", n)
}

// checkOwnerToken fails an archive nobody could log into. Restoring state that no
// credential can reach is not a restore, and it is the one failure an operator would
// only find at the worst moment.
func checkOwnerToken(ctx context.Context, conn *pgx.Conn, v *Verdict) {
	var n int
	err := conn.QueryRow(ctx, `SELECT count(*) FROM api_tokens_heads
		WHERE role = 'owner' AND NOT deleted AND (expires_at IS NULL OR expires_at > now())`).Scan(&n)
	if err != nil {
		v.add("owner-token", false, "count owner tokens: %v", err)
		return
	}
	v.OwnerTokens = n
	if n == 0 {
		v.add("owner-token", false, "no live owner token: nobody could administer the restored install")
		return
	}
	v.add("owner-token", true, "%d live owner token(s)", n)
}

// checkNodeCA fails an archive that carried no node CA, or one that cannot be loaded.
// Without it every agent has to re-register, which is the difference between a restore
// and a rebuild. The pin lets an operator compare it with the live install's.
func checkNodeCA(cfg Config, v *Verdict, dataDir string) {
	dir := filepath.Join(dataDir, "pki")
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		v.add("node-ca", false, "read the restored CA directory: %v", err)
		return
	}
	if len(entries) == 0 {
		v.add("node-ca", false, "the archive carried no node CA: every agent would have to re-register")
		return
	}
	// Every file is present, so this loads them; it would only create a CA for an
	// empty directory, which the check above has already refused.
	authority, err := pki.LoadOrCreate(dataDir, cfg.Now)
	if err != nil {
		v.add("node-ca", false, "the restored node CA does not load: %v", err)
		return
	}
	v.CAPin = authority.CACertSHA256()
	block, _ := pem.Decode(authority.CACertPEM())
	if block == nil {
		v.add("node-ca", false, "the restored node CA is not PEM")
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		v.add("node-ca", false, "parse the restored node CA: %v", err)
		return
	}
	v.CAExpires = cert.NotAfter
	if now := cfg.now(); cert.NotAfter.Before(now) {
		v.add("node-ca", false, "the restored node CA expired on %s", cert.NotAfter.Format("2006-01-02"))
		return
	}
	v.add("node-ca", true, "node CA restored, pin %s, expires %s", v.CAPin, cert.NotAfter.Format("2006-01-02"))
}

// checkSecrets reads back every live secret ref at the version Postgres records for
// it. This is the end-to-end proof that restore rebuilt OpenBao's version history:
// a value at the wrong version number is a secret the agents would resolve wrongly.
// Values are read and discarded — none reaches the verdict or a log (rule 3).
func checkSecrets(ctx context.Context, conn *pgx.Conn, cfg Config, v *Verdict) {
	rows, err := conn.Query(ctx, `SELECT s.handle, r.value_version
		FROM secret_refs_heads s JOIN secret_refs_revisions r ON r.id = s.revision_id
		WHERE NOT s.deleted`)
	if err != nil {
		v.add("secrets", false, "list secret refs: %v", err)
		return
	}
	type ref struct {
		Handle  string
		Version int
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ref])
	if err != nil {
		v.add("secrets", false, "list secret refs: %v", err)
		return
	}
	if len(refs) == 0 {
		v.add("secrets", true, "the archive holds no live secret refs")
		return
	}
	if cfg.Bao == nil {
		v.add("secrets", false, "%d secret refs restored but the drill has no KV mount to read them from", len(refs))
		return
	}
	var bad []string
	for _, r := range refs {
		path, ok := strings.CutPrefix(r.Handle, openbao.HandlePrefix)
		if !ok {
			bad = append(bad, fmt.Sprintf("%s (not an OpenBao handle)", r.Handle))
			continue
		}
		if _, err := cfg.Bao.Get(ctx, path, r.Version); err != nil {
			bad = append(bad, fmt.Sprintf("%s version %d (%v)", path, r.Version, err))
			continue
		}
		v.Secrets++
	}
	if len(bad) > 0 {
		v.add("secrets", false, "%d of %d secret refs did not read back: %s",
			len(bad), len(refs), quoteList(bad))
		return
	}
	v.add("secrets", true, "all %d secret refs read back at the recorded version", len(refs))
}
