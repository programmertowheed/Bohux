//go:build integration

package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
)

// fakeSSH stands in for ssh: it accepts only the expected key file, records that it
// ran, and serves the repository locally, as sshd would run git-upload-pack.
const fakeSSH = `#!/bin/sh
key=""
while [ $# -gt 1 ]; do
  case "$1" in
    -i) key="$2"; shift 2 ;;
    -o|-p) shift 2 ;;
    *) break ;;
  esac
done
shift # the host
cmp -s "$key" "$BOHUX_TEST_EXPECTED_KEY" || { echo "permission denied (publickey)" >&2; exit 255; }
[ "$(stat -c %a "$key")" = 600 ] || { echo "key file mode $(stat -c %a "$key")" >&2; exit 255; }
echo "$key" > "$BOHUX_TEST_SSH_RECORD"
exec sh -c "$*"
`

// TestBuilderDeployKey fetches over SSH with a sealed deploy key and forwards it to
// RUN --mount=type=ssh, leaving no key on disk (ADR-0041).
func TestBuilderDeployKey(t *testing.T) {
	if err := exec.Command("docker", "buildx", "version").Run(); err != nil {
		t.Skip("no docker buildx: ", err)
	}
	ctx := context.Background()
	repo, commit := gitRepo(t, map[string]string{
		"Dockerfile": "FROM busybox:1.36\nRUN --mount=type=ssh test -S \"$SSH_AUTH_SOCK\"\nCMD [\"true\"]\n",
	})

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(block))
	tmp := t.TempDir()
	expected, record, sshBin := filepath.Join(tmp, "expected"), filepath.Join(tmp, "record"), filepath.Join(tmp, "ssh")
	if err := os.WriteFile(expected, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sshBin, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}

	signer, err := pki.LoadOrCreate(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	st := testState(t, signer)
	plain, err := proto.Marshal(&agentv1.SecretValues{SshKey: key})
	if err != nil {
		t.Fatal(err)
	}
	var pub [32]byte
	copy(pub[:], st.sealingKey.PublicKey().Bytes())
	sealed, err := box.SealAnonymous(nil, plain, &pub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	b := &Builder{WorkDir: work, Secrets: st, SSH: sshBin,
		Env: []string{"BOHUX_TEST_EXPECTED_KEY=" + expected, "BOHUX_TEST_SSH_RECORD=" + record}}
	req := func(release string, sealed []byte) *agentv1.BuildRequest {
		return &agentv1.BuildRequest{ReleaseId: release, AppId: "app-1", RepoUrl: "git@git.example.invalid:" + repo,
			Ref: "v1", SshKey: &bohuxv1.SecretRef{Key: "DEPLOY_KEY", Handle: "openbao:deploy"}, SealedSecrets: sealed}
	}

	res := b.Build(ctx, req("rel-key-1", sealed))
	if res.GetFailure() != nil {
		t.Fatalf("build failed: %v", res.GetFailure())
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", res.GetImageId()).Run() })
	if res.GetCommit() != commit {
		t.Errorf("commit = %s, want %s", res.GetCommit(), commit)
	}
	used, err := os.ReadFile(record)
	if err != nil {
		t.Fatal("git did not fetch through ssh with the key: ", err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(used))); !os.IsNotExist(err) {
		t.Errorf("the deploy key file is still on disk: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(work, "key-*")); len(left) != 0 {
		t.Errorf("key directories left behind: %v", left)
	}

	tests := []struct {
		name string
		b    *Builder
		r    *agentv1.BuildRequest
		want string
	}{
		{"no way to open the key", &Builder{WorkDir: t.TempDir()}, req("rel-key-2", sealed), BuildInvalid},
		{"key named but not sent", b, req("rel-key-3", nil), BuildInvalid},
		{"sealed for another node", b, req("rel-key-4", []byte("not sealed for this node")), BuildInvalid},
		{"wrong key", &Builder{WorkDir: t.TempDir(), Secrets: st, SSH: sshBin,
			Env: []string{"BOHUX_TEST_EXPECTED_KEY=" + sshBin, "BOHUX_TEST_SSH_RECORD=" + record}},
			req("rel-key-5", sealed), BuildFetchFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.b.Build(ctx, tt.r).GetFailure().GetReason(); got != tt.want {
				t.Errorf("failure = %q, want %q", got, tt.want)
			}
		})
	}
}
