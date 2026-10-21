// Package backup writes and restores Bohux's own state as one archive (ADR-0020 item
// 7, ADR-0029): a logical copy of every Postgres table, every version of every secret
// value in OpenBao, and the node CA, as a tar stream encrypted with age. The archive is
// never written unencrypted, and nothing here logs a value (rule 3).
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"

	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
)

// formatVersion is the archive layout; restore refuses others.
const formatVersion = 1

const (
	manifestName = "manifest.json"
	secretsName  = "openbao.json"
	tablePrefix  = "postgres/"
	pkiPrefix    = "pki/"
)

var (
	// ErrNotEmpty means the restore target already holds state.
	ErrNotEmpty = errors.New("restore target is not empty")
	// ErrBadArchive means the archive is not one this version can read.
	ErrBadArchive = errors.New("not a Bohux backup this version can restore")
)

// Manifest describes an archive. It is the first entry.
type Manifest struct {
	Format       int       `json:"format"`
	BohuxVersion string    `json:"bohux_version"`
	Migration    int64     `json:"migration"`
	CreatedAt    time.Time `json:"created_at"`
	Tables       []string  `json:"tables"`
}

// secretVersion is one version of one secret path; Gone versions have no value.
type secretVersion struct {
	Version int    `json:"version"`
	Value   string `json:"value,omitempty"`
	Gone    bool   `json:"gone,omitempty"`
}

// Source is what a backup reads.
type Source struct {
	// AdminDSN is a superuser or BYPASSRLS role on Bohux's database: row-level
	// security hides every row from the application role outside a tenant.
	AdminDSN string
	// Bao holds secret values; nil when secrets are not configured.
	Bao *openbao.Client
	// DataDir is the control plane's data directory; its pki/ is backed up.
	DataDir string
}

// Write streams an encrypted archive of src to w for the recipients.
func Write(ctx context.Context, w io.Writer, to []age.Recipient, src Source) (*Manifest, error) {
	if len(to) == 0 {
		return nil, errors.New("backup: at least one recipient is required")
	}
	conn, err := pgx.Connect(ctx, src.AdminDSN)
	if err != nil {
		return nil, fmt.Errorf("backup: connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var current int64
	if err := conn.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version
		WHERE is_applied`).Scan(&current); err != nil {
		return nil, fmt.Errorf("backup: read migration version: %w", err)
	}
	tables, err := listTables(ctx, conn)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Format: formatVersion, BohuxVersion: version.Version, Migration: current,
		CreatedAt: time.Now().UTC(), Tables: tables}

	enc, err := age.Encrypt(w, to...)
	if err != nil {
		return nil, fmt.Errorf("backup: encrypt: %w", err)
	}
	tw := tar.NewWriter(enc)
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("backup: encode manifest: %w", err)
	}
	if err := put(tw, manifestName, manifest, 0o600); err != nil {
		return nil, err
	}
	// One snapshot for every table, so rows that refer to each other agree.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("backup: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, t := range tables {
		var buf bytes.Buffer
		if _, err := tx.Conn().PgConn().CopyTo(ctx, &buf, "COPY public."+pgx.Identifier{t}.Sanitize()+" TO STDOUT"); err != nil {
			return nil, fmt.Errorf("backup: copy %s: %w", t, err)
		}
		if err := put(tw, tablePrefix+t+".copy", buf.Bytes(), 0o600); err != nil {
			return nil, err
		}
	}
	if src.Bao != nil {
		values, err := exportSecrets(ctx, tx, src.Bao)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(values)
		if err != nil {
			return nil, errors.New("backup: encode secret values")
		}
		if err := put(tw, secretsName, b, 0o600); err != nil {
			return nil, err
		}
	}
	if err := putDir(tw, filepath.Join(src.DataDir, "pki"), pkiPrefix); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("backup: finish archive: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("backup: finish encryption: %w", err)
	}
	return m, nil
}

func listTables(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'goose_db_version' ORDER BY tablename`)
	if err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("backup: list tables: %w", err)
	}
	return tables, nil
}

// exportSecrets reads every version of every secret path of the tenants in the
// database, so restore can rebuild the same version numbers Postgres records.
func exportSecrets(ctx context.Context, tx pgx.Tx, bao *openbao.Client) (map[string][]secretVersion, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM tenants_heads`)
	if err != nil {
		return nil, fmt.Errorf("backup: list tenants: %w", err)
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("backup: list tenants: %w", err)
	}
	out := map[string][]secretVersion{}
	for _, tenant := range tenants {
		prefix := "bohux/" + tenant + "/"
		keys, err := bao.List(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("backup: list secrets: %w", err)
		}
		for _, k := range keys {
			if strings.HasSuffix(k, "/") {
				continue
			}
			path := prefix + k
			versions, current, err := bao.Versions(ctx, path)
			if err != nil {
				return nil, fmt.Errorf("backup: read secret metadata: %w", err)
			}
			for n := 1; n <= current; n++ {
				sv := secretVersion{Version: n}
				if v, ok := versions[n]; !ok || v.Gone {
					sv.Gone = true
				} else if sv.Value, err = bao.Get(ctx, path, n); err != nil {
					return nil, fmt.Errorf("backup: read secret: %w", err)
				}
				out[path] = append(out[path], sv)
			}
		}
	}
	return out, nil
}

func put(tw *tar.Writer, name string, b []byte, mode int64) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	if _, err := tw.Write(b); err != nil {
		return fmt.Errorf("backup: write %s: %w", name, err)
	}
	return nil
}

// putDir adds the regular files directly in dir under prefix.
func putDir(tw *tar.Writer, dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: read %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("backup: read %s: %w", e.Name(), err)
		}
		if err := put(tw, prefix+e.Name(), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Target is where a restore writes. Every part must be empty.
type Target struct {
	// AdminDSN is a superuser or BYPASSRLS role on the target database.
	AdminDSN string
	// AppDSN is the role Bohux runs as; it creates and owns the schema.
	AppDSN string
	// Bao receives secret values; required if the archive has any.
	Bao *openbao.Client
	// DataDir receives pki/.
	DataDir string
}

// Restore decrypts an archive with the identities and restores it into dst, which
// must be empty: a fresh database, no secret paths, no node CA.
func Restore(ctx context.Context, r io.Reader, ids []age.Identity, dst Target) (*Manifest, error) {
	dec, err := age.Decrypt(r, ids...)
	if err != nil {
		return nil, fmt.Errorf("restore: decrypt (wrong identity?): %w", err)
	}
	tr := tar.NewReader(dec)
	h, err := tr.Next()
	if err != nil || h.Name != manifestName {
		return nil, ErrBadArchive
	}
	var m Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil || m.Format != formatVersion {
		return nil, ErrBadArchive
	}
	latest, err := store.LatestMigration()
	if err != nil {
		return nil, err
	}
	if m.Migration > latest {
		return nil, fmt.Errorf("restore: the archive needs migration %d; this binary has %d: %w", m.Migration, latest, ErrBadArchive)
	}
	conn, err := pgx.Connect(ctx, dst.AdminDSN)
	if err != nil {
		return nil, fmt.Errorf("restore: connect: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if existing, err := listTables(ctx, conn); err != nil {
		return nil, err
	} else if len(existing) > 0 {
		return nil, fmt.Errorf("restore: database has %d tables: %w", len(existing), ErrNotEmpty)
	}
	pki := filepath.Join(dst.DataDir, "pki")
	if entries, err := os.ReadDir(pki); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("restore: %s holds a node CA: %w", pki, ErrNotEmpty)
	}
	if err := store.MigrateTo(ctx, dst.AppDSN, m.Migration); err != nil {
		return nil, err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("restore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Rows go back as they were: no triggers (append-only guards) and no foreign-key
	// checks while tables fill in any order.
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return nil, fmt.Errorf("restore: %w", err)
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("restore: read archive: %w", err)
		}
		switch name := h.Name; {
		case strings.HasPrefix(name, tablePrefix):
			table := strings.TrimSuffix(strings.TrimPrefix(name, tablePrefix), ".copy")
			if !slices.Contains(m.Tables, table) {
				return nil, fmt.Errorf("restore: unexpected table %q: %w", table, ErrBadArchive)
			}
			if _, err := tx.Conn().PgConn().CopyFrom(ctx, tr, "COPY public."+pgx.Identifier{table}.Sanitize()+" FROM STDIN"); err != nil {
				return nil, fmt.Errorf("restore: copy %s: %w", table, err)
			}
		case name == secretsName:
			if err := importSecrets(ctx, tr, dst.Bao); err != nil {
				return nil, err
			}
		case strings.HasPrefix(name, pkiPrefix):
			if err := writeFile(pki, strings.TrimPrefix(name, pkiPrefix), tr); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("restore: unexpected entry %q: %w", name, ErrBadArchive)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("restore: commit: %w", err)
	}
	return &m, nil
}

// importSecrets writes every version back in order to empty paths, so version n is
// again version n, then destroys the placeholders for versions that had no value.
func importSecrets(ctx context.Context, r io.Reader, bao *openbao.Client) error {
	var values map[string][]secretVersion
	if err := json.NewDecoder(r).Decode(&values); err != nil {
		return fmt.Errorf("restore: decode secret values: %w", ErrBadArchive)
	}
	if len(values) > 0 && bao == nil {
		return errors.New("restore: the archive holds secret values but no OpenBao is configured")
	}
	for path, versions := range values {
		if _, _, err := bao.Versions(ctx, path); !errors.Is(err, openbao.ErrNotFound) {
			if err == nil {
				err = ErrNotEmpty
			}
			return fmt.Errorf("restore: secret path is not empty: %w", err)
		}
		var gone []int
		for _, v := range versions {
			got, err := bao.Put(ctx, path, v.Value)
			if err != nil {
				return fmt.Errorf("restore: write secret: %w", err)
			}
			if got != v.Version {
				return fmt.Errorf("restore: secret version %d came back as %d", v.Version, got)
			}
			if v.Gone {
				gone = append(gone, v.Version)
			}
		}
		if len(gone) > 0 {
			if err := bao.DestroyVersions(ctx, path, gone); err != nil {
				return fmt.Errorf("restore: destroy placeholder versions: %w", err)
			}
		}
	}
	return nil
}

func writeFile(dir, name string, r io.Reader) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("restore: bad file name %q: %w", name, ErrBadArchive)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("restore: write %s: %w", name, err)
	}
	return f.Close()
}
