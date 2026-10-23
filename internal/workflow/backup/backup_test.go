package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/bohumix/bohux/internal/backup"
)

func TestWorkflowFailedWriteNeverPrunes(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	var a *Activities
	env.OnActivity(a.WriteBackup, mock.Anything).Return(Result{}, errors.New("database unreachable"))
	pruned := false
	env.OnActivity(a.PruneBackups, mock.Anything).Return(func(context.Context) ([]string, error) {
		pruned = true
		return nil, nil
	})
	env.ExecuteWorkflow(Workflow)
	if env.GetWorkflowError() == nil {
		t.Fatal("workflow succeeded; want the write failure")
	}
	if pruned {
		t.Fatal("pruned after a failed write: the old archives are all that is left")
	}
}

func TestWorkflowWritesThenPrunes(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	var a *Activities
	var order []string
	env.OnActivity(a.WriteBackup, mock.Anything).Return(func(context.Context) (Result, error) {
		order = append(order, "write")
		return Result{File: "/b/bohux-20261021T031700Z.backup", Bytes: 10, Migration: 17}, nil
	})
	env.OnActivity(a.PruneBackups, mock.Anything).Return(func(context.Context) ([]string, error) {
		order = append(order, "prune")
		return []string{"bohux-20261013T031700Z.backup"}, nil
	})
	env.ExecuteWorkflow(Workflow)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var res Result
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"write", "prune"}) || len(res.Pruned) != 1 || res.Migration != 17 {
		t.Fatalf("order %v, result %+v", order, res)
	}
}

func recipient(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Recipient().String()
}

func TestWriteBackup(t *testing.T) {
	at := time.Date(2026, 10, 21, 3, 17, 0, 0, time.UTC)
	tests := []struct {
		name      string
		recipient string
		write     func(context.Context, io.Writer, []age.Recipient, backup.Source) (*backup.Manifest, error)
		wantErr   bool
		wantFiles []string
	}{
		{
			name: "a failure halfway leaves no file",
			write: func(_ context.Context, w io.Writer, _ []age.Recipient, _ backup.Source) (*backup.Manifest, error) {
				_, _ = w.Write([]byte("half an archive"))
				return nil, errors.New("connection reset")
			},
			wantErr: true,
		},
		{
			name:      "a recipient that is not an age key fails before writing",
			recipient: "age1-not-a-key",
			wantErr:   true,
		},
		{
			name: "success renames the finished archive",
			write: func(_ context.Context, w io.Writer, _ []age.Recipient, _ backup.Source) (*backup.Manifest, error) {
				_, _ = w.Write([]byte("archive"))
				return &backup.Manifest{Migration: 17}, nil
			},
			wantFiles: []string{"bohux-20261021T031700Z.backup"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "backups")
			r := tt.recipient
			if r == "" {
				r = recipient(t)
			}
			a := &Activities{Recipient: r, Dir: dir, Write: tt.write, Now: func() time.Time { return at }}
			res, err := a.WriteBackup(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			entries, _ := os.ReadDir(dir)
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			if !slices.Equal(got, tt.wantFiles) {
				t.Fatalf("files %v, want %v", got, tt.wantFiles)
			}
			if !tt.wantErr {
				st, err := os.Stat(res.File)
				if err != nil || st.Mode().Perm() != 0o600 || res.Bytes != 7 || res.Migration != 17 {
					t.Fatalf("result %+v, stat %v %v", res, st, err)
				}
			}
		})
	}
}

func TestPruneBackups(t *testing.T) {
	tests := []struct {
		name  string
		keep  int
		files []string
		want  []string
	}{
		{
			name: "keeps the newest and leaves other files alone",
			keep: 2,
			files: []string{"bohux-20261018T031700Z.backup", "bohux-20261019T031700Z.backup",
				"bohux-20261020T031700Z.backup", "bohux-20261021T031700Z.backup", "notes.txt", "bohux-manual.backup"},
			want: []string{"bohux-20261020T031700Z.backup", "bohux-20261021T031700Z.backup", "bohux-manual.backup", "notes.txt"},
		},
		{
			name:  "removes leftover partial files",
			keep:  2,
			files: []string{"bohux-20261021T031700Z.backup", "bohux-20261020T031700Z.backup.partial", "x.partial"},
			want:  []string{"bohux-20261021T031700Z.backup", "x.partial"},
		},
		{
			name:  "zero keep means the default",
			files: []string{"bohux-20261021T031700Z.backup"},
			want:  []string{"bohux-20261021T031700Z.backup"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a := &Activities{Dir: dir, Keep: tt.keep}
			if _, err := a.PruneBackups(context.Background()); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			var got []string
			for _, e := range entries {
				got = append(got, e.Name())
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("left %v, want %v", got, tt.want)
			}
		})
	}
}
