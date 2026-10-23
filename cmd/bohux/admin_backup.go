package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"

	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/secrets/openbao"
)

const envAdminDatabaseURL = "BOHUX_ADMIN_DATABASE_URL"

// stateFlags are what backup and restore both need: a privileged database URL, the
// data directory, and OpenBao (ADR-0029).
type stateFlags struct {
	adminDB, dataDir, baoAddr, baoToken, baoMount *string
}

func addStateFlags(fs *flag.FlagSet, getenv func(string) string) stateFlags {
	return stateFlags{
		adminDB: fs.String("admin-database-url", getenv(envAdminDatabaseURL),
			"Postgres DSN of a superuser or BYPASSRLS role on Bohux's database (env "+envAdminDatabaseURL+")"),
		dataDir: fs.String("data-dir", envOr(getenv, envDataDir, defaultDataDir),
			"control plane data directory (env "+envDataDir+")"),
		baoAddr:  fs.String("openbao-address", getenv(envOpenBaoAddr), "OpenBao URL (env "+envOpenBaoAddr+")"),
		baoToken: fs.String("openbao-token-file", getenv(envOpenBaoToken), "file holding the OpenBao token (env "+envOpenBaoToken+")"),
		baoMount: fs.String("openbao-kv-mount", envOr(getenv, envOpenBaoMount, defaultOpenBaoMount),
			"OpenBao KV v2 mount (env "+envOpenBaoMount+")"),
	}
}

func (f stateFlags) bao() (*openbao.Client, error) {
	if *f.baoAddr == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(*f.baoToken)
	if err != nil {
		return nil, fmt.Errorf("read OpenBao token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, errors.New("OpenBao token file is empty")
	}
	return &openbao.Client{Addr: *f.baoAddr, Token: token, Mount: *f.baoMount}, nil
}

// adminBackup writes an encrypted archive of Bohux's state to a new file.
func adminBackup(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	fs := newFlagSet("admin backup")
	st := addStateFlags(fs, getenv)
	recipient := fs.String("recipient", "", "age public key (age1…) the archive is encrypted to (required)")
	out := fs.String("out", "", "archive file to create (required)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("admin backup: %w", err)
	}
	if *st.adminDB == "" || *recipient == "" || *out == "" {
		return errors.New("admin backup: --admin-database-url (or " + envAdminDatabaseURL + "), --recipient, and --out are required")
	}
	to, err := age.ParseX25519Recipient(*recipient)
	if err != nil {
		return fmt.Errorf("admin backup: --recipient: %w", err)
	}
	bao, err := st.bao()
	if err != nil {
		return fmt.Errorf("admin backup: %w", err)
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("admin backup: %w", err)
	}
	m, err := backup.Write(ctx, f, []age.Recipient{to}, backup.Source{AdminDSN: *st.adminDB, Bao: bao, DataDir: *st.dataDir})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(*out)
		return fmt.Errorf("admin backup: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "Backup written to %s (%d tables, migration %d).\n", *out, len(m.Tables), m.Migration)
	return err
}

// adminRestore restores an archive onto an empty database, OpenBao, and data dir.
func adminRestore(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	fs := newFlagSet("admin restore")
	st := addStateFlags(fs, getenv)
	appDB := fs.String("database-url", getenv(envDatabaseURL), "Postgres DSN Bohux runs as; it will own the schema (env "+envDatabaseURL+")")
	identity := fs.String("identity", "", "age identity file that decrypts the archive (required)")
	in := fs.String("in", "", "archive to restore (required)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("admin restore: %w", err)
	}
	if *st.adminDB == "" || *appDB == "" || *identity == "" || *in == "" {
		return errors.New("admin restore: --admin-database-url, --database-url, --identity, and --in are required")
	}
	idf, err := os.Open(*identity)
	if err != nil {
		return fmt.Errorf("admin restore: %w", err)
	}
	ids, err := age.ParseIdentities(idf)
	_ = idf.Close()
	if err != nil {
		return fmt.Errorf("admin restore: --identity: %w", err)
	}
	bao, err := st.bao()
	if err != nil {
		return fmt.Errorf("admin restore: %w", err)
	}
	f, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("admin restore: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, err := backup.Restore(ctx, f, ids, backup.Target{AdminDSN: *st.adminDB, AppDSN: *appDB, Bao: bao, DataDir: *st.dataDir})
	if err != nil {
		return fmt.Errorf("admin restore: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "Restored a backup from %s (Bohux %s, migration %d). Start the control plane to finish migrating.\n",
		m.CreatedAt.Format("2006-01-02 15:04 MST"), m.BohuxVersion, m.Migration)
	return err
}
