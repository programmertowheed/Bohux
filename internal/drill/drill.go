// Package drill proves that a Bohux backup archive can actually be restored
// (ADR-0051). It restores an archive into throwaway state — a scratch database created
// for the run, a temporary data directory, and a KV mount of its own — checks that what
// came back is a usable install, and destroys all of it again, pass or fail. Nothing it
// touches is live state, and no secret value reaches a log or a verdict (rule 3).
package drill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/bohumix/bohux/internal/backup"
	"github.com/bohumix/bohux/internal/secrets/openbao"
)

const (
	// DefaultSchedule is 04:47 UTC on Sundays: after the nightly archive (ADR-0033),
	// off the hour, and out of the way of a working week.
	DefaultSchedule = "47 4 * * 0"
	// dbPrefix starts every scratch database's name, so a leaked one is recognisable.
	dbPrefix = "bohux_drill_"
	// nameLayout dates a scratch database and a verdict file; UTC sorts lexically.
	nameLayout = "20060102t150405z"
)

// ErrFailed means the archive restored but the install that came back failed a check.
var ErrFailed = errors.New("restore drill failed")

// Config is one drill run: the archive to prove, and the throwaway state to prove it
// in. Neither role needs a single grant in the live database (ADR-0051 item 3).
type Config struct {
	// Archive is the archive file to restore.
	Archive string
	// Identities decrypt it. The drill needs a private key; the backup does not.
	Identities []age.Identity
	// ScratchDSN is the `bohux_drill` role: LOGIN CREATEDB and no BYPASSRLS, so it
	// can create the scratch database and store.MigrateTo accepts it (ADR-0021).
	ScratchDSN string
	// AdminDSN is the `bohux_drill_admin` role: LOGIN BYPASSRLS, because every table
	// forces row-level security and its owner cannot COPY into one.
	AdminDSN string
	// Bao is the drill's own KV v2 mount, wiped before and after the run. Nil only
	// when the archive holds no secret values.
	Bao *openbao.Client
	// LiveMount is the mount holding live secrets, so the drill can refuse to touch
	// it. Empty skips the check.
	LiveMount string
	// Now replaces time.Now in tests.
	Now func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// validate refuses a configuration that would touch live state or cannot run.
func (c Config) validate() error {
	switch {
	case c.Archive == "":
		return errors.New("an archive to restore is required")
	case len(c.Identities) == 0:
		return errors.New("an age identity is required: a drill has to decrypt the archive")
	case c.ScratchDSN == "":
		return errors.New("the scratch role's DSN is required")
	case c.AdminDSN == "":
		return errors.New("the admin role's DSN is required")
	}
	if c.Bao != nil && c.LiveMount != "" && c.Bao.Mount == c.LiveMount {
		return fmt.Errorf("the drill KV mount is the live one (%q); a drill wipes its mount", c.Bao.Mount)
	}
	return nil
}

// Check is one assertion about the restored install.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Verdict is what one run found. It holds names, counts and times — never a value.
type Verdict struct {
	// Archive is the file that was restored.
	Archive string `json:"archive"`
	// Database is the scratch database the run used, and dropped.
	Database   string    `json:"database"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// CreatedAt, BohuxVersion and Migration come from the archive's manifest.
	CreatedAt    time.Time `json:"created_at"`
	BohuxVersion string    `json:"bohux_version,omitempty"`
	Migration    int64     `json:"migration,omitempty"`
	// Tables is how many the manifest named; Rows and PerTable are what came back.
	Tables   int              `json:"tables,omitempty"`
	Rows     int64            `json:"rows,omitempty"`
	PerTable map[string]int64 `json:"per_table,omitempty"`
	// Tenants and OwnerTokens are what makes a restored install usable.
	Tenants     int `json:"tenants,omitempty"`
	OwnerTokens int `json:"owner_tokens,omitempty"`
	// Secrets is how many secret refs were read back at the version Postgres records.
	Secrets int `json:"secrets,omitempty"`
	// CAPin is the restored node CA's pin; agents trust the CA with this hash, so an
	// operator can compare it with the live install's.
	CAPin     string    `json:"ca_pin,omitempty"`
	CAExpires time.Time `json:"ca_expires"`
	// Checks is every assertion, in the order it ran.
	Checks []Check `json:"checks"`
}

// add records one check.
func (v *Verdict) add(name string, ok bool, format string, args ...any) {
	v.Checks = append(v.Checks, Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
}

// Failed names the checks that did not pass.
func (v *Verdict) Failed() []string {
	var out []string
	for _, c := range v.Checks {
		if !c.OK {
			out = append(out, c.Name)
		}
	}
	return out
}

// OK reports whether every check passed.
func (v *Verdict) OK() bool { return len(v.Failed()) == 0 }

// Run drills cfg.Archive and returns what it found. The verdict comes back even when
// the drill fails, so a caller can record it; the error is ErrFailed when the archive
// restored but a check did not pass, and a wrapped cause when it could not be
// restored at all. Every piece of throwaway state is removed before Run returns.
func Run(ctx context.Context, cfg Config) (*Verdict, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("drill: %w", err)
	}
	started := cfg.now().UTC()
	v := &Verdict{Archive: cfg.Archive, Database: dbPrefix + started.Format(nameLayout), StartedAt: started}

	dataDir, err := os.MkdirTemp("", "bohux-drill-")
	if err != nil {
		return nil, fmt.Errorf("drill: data directory: %w", err)
	}
	// An interrupted earlier run can leave paths behind, and restore refuses a mount
	// that is not empty, so the mount is wiped going in as well as coming out.
	if err := wipeMount(ctx, cfg.Bao); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("drill: prepare the drill KV mount: %w", err)
	}
	if err := createDatabase(ctx, cfg.ScratchDSN, v.Database); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("drill: %w", err)
	}

	runErr := drill(ctx, cfg, v, dataDir)

	// Teardown runs whatever happened above, and on a cancelled context: throwaway
	// state that outlives the run costs disk and makes the next run refuse.
	if err := teardown(context.WithoutCancel(ctx), cfg, v.Database, dataDir); err != nil {
		v.add("cleanup", false, "%v", err)
		slog.ErrorContext(ctx, "drill: throwaway state left behind", "database", v.Database, "err", err)
	} else {
		v.add("cleanup", true, "scratch database, data directory and KV mount removed")
	}
	v.FinishedAt = cfg.now().UTC()

	if runErr != nil {
		return v, runErr
	}
	if failed := v.Failed(); len(failed) > 0 {
		return v, fmt.Errorf("%w: %s", ErrFailed, strings.Join(failed, ", "))
	}
	slog.InfoContext(ctx, "restore drill passed", "archive", cfg.Archive, "migration", v.Migration,
		"tables", v.Tables, "rows", v.Rows, "secrets", v.Secrets, "ca_pin", v.CAPin,
		"took", v.FinishedAt.Sub(v.StartedAt).Round(time.Second).String())
	return v, nil
}

// drill restores the archive into the scratch state and checks what came back.
func drill(ctx context.Context, cfg Config, v *Verdict, dataDir string) error {
	appDSN, err := withDatabase(cfg.ScratchDSN, v.Database)
	if err != nil {
		return fmt.Errorf("drill: scratch DSN: %w", err)
	}
	adminDSN, err := withDatabase(cfg.AdminDSN, v.Database)
	if err != nil {
		return fmt.Errorf("drill: admin DSN: %w", err)
	}
	role, err := roleOf(cfg.AdminDSN)
	if err != nil {
		return fmt.Errorf("drill: admin DSN: %w", err)
	}
	// Restore migrates as the owner and then COPYs as the admin role, so the grant
	// has to cover tables that do not exist yet: a default privilege, not a GRANT.
	if err := grantToAdmin(ctx, appDSN, role); err != nil {
		return fmt.Errorf("drill: %w", err)
	}

	f, err := os.Open(cfg.Archive)
	if err != nil {
		return fmt.Errorf("drill: %w", err)
	}
	defer func() { _ = f.Close() }()
	m, err := backup.Restore(ctx, f, cfg.Identities, backup.Target{
		AdminDSN: adminDSN, AppDSN: appDSN, Bao: cfg.Bao, DataDir: dataDir,
	})
	if err != nil {
		v.add("restore", false, "%v", err)
		return fmt.Errorf("drill: restore %s: %w", cfg.Archive, err)
	}
	v.add("restore", true, "%d tables, migration %d, written %s",
		len(m.Tables), m.Migration, m.CreatedAt.Format(time.RFC3339))
	v.CreatedAt, v.BohuxVersion, v.Migration, v.Tables = m.CreatedAt, m.BohuxVersion, m.Migration, len(m.Tables)
	return inspect(ctx, cfg, v, m, adminDSN, dataDir)
}

// teardown removes every piece of throwaway state. It reports the problems it could
// not fix rather than stopping at the first.
func teardown(ctx context.Context, cfg Config, database, dataDir string) error {
	var errs []error
	if err := dropDatabase(ctx, cfg.ScratchDSN, database); err != nil {
		errs = append(errs, err)
	}
	if err := os.RemoveAll(dataDir); err != nil {
		errs = append(errs, fmt.Errorf("remove %s: %w", dataDir, err))
	}
	if err := wipeMount(ctx, cfg.Bao); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// wipeMount destroys every secret path in the drill's own mount. A nil client is a
// drill configured without secrets, and nothing to wipe.
func wipeMount(ctx context.Context, bao *openbao.Client) error {
	if bao == nil {
		return nil
	}
	paths, err := walk(ctx, bao, "")
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := bao.Destroy(ctx, p); err != nil {
			return fmt.Errorf("wipe the drill KV mount: %w", err)
		}
	}
	return nil
}

// walk lists every secret path under prefix, descending into sub-prefixes.
func walk(ctx context.Context, bao *openbao.Client, prefix string) ([]string, error) {
	keys, err := bao.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list the drill KV mount: %w", err)
	}
	var out []string
	for _, k := range keys {
		if sub, ok := strings.CutSuffix(k, "/"); ok {
			nested, err := walk(ctx, bao, prefix+sub+"/")
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			continue
		}
		out = append(out, prefix+k)
	}
	return out, nil
}
