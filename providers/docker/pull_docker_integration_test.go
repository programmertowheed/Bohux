//go:build integration

package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// TestPrivateImageDocker pulls from a registry that requires a login, with the
// credential given for the one command only (ADR-0041).
func TestPrivateImageDocker(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("no Docker daemon: ", err)
	}
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())

	// A registry with one user, its password file baked into the image.
	const user, password = "bohux", "pull-sekrit"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "htpasswd"), []byte(user+":"+string(hash)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM registry:2\nCOPY htpasswd /auth/htpasswd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regImage := "bohux-pull-registry:" + suffix
	dockerOut(t, "build", "-q", "-t", regImage, dir)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", regImage).Run() })
	reg := "bohux-pull-registry-" + suffix
	dockerOut(t, "run", "-d", "--name", reg, "-p", "127.0.0.1::5000",
		"-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=bohux",
		"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd", regImage)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", reg).Run() })
	_, port, _ := strings.Cut(dockerOut(t, "port", reg, "5000/tcp"), ":")
	host := "localhost:" + strings.TrimSpace(strings.Split(port, "\n")[0])

	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	config := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, auth)
	login := t.TempDir()
	if err := os.WriteFile(filepath.Join(login, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	// Push an image, then remove it locally so Apply must pull it.
	app := t.TempDir()
	if err := os.WriteFile(filepath.Join(app, "Dockerfile"),
		[]byte("FROM busybox:1.36\nCMD [\"sleep\", \"3600\"]\nLABEL io.bohux.test="+suffix+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := host + "/private:" + suffix
	dockerOut(t, "build", "-q", "-t", tag, app)
	var pushErr error
	for i := 0; i < 20; i++ { // the registry may still be starting
		if out, err := exec.Command("docker", "--config", login, "push", "-q", tag).CombinedOutput(); err != nil {
			pushErr = fmt.Errorf("%w: %s", err, out)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		pushErr = nil
		break
	}
	if pushErr != nil {
		t.Fatal("push: ", pushErr)
	}
	image := dockerOut(t, "inspect", "--format", "{{index .RepoDigests 0}}", tag)
	dockerOut(t, "rmi", "-f", tag)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	p, err := New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.ResourceRef{Environment: "env-" + suffix, App: "app-" + suffix, AppSlug: "private", EnvironmentSlug: "test"}
	spec := provider.Spec{Ref: ref, Release: "r1", Image: image, Hash: "h1", Runtime: &bohuxv1.Runtime{Replicas: 1}}
	t.Cleanup(func() { _ = p.Destroy(ctx, ref) })

	if _, err := p.Apply(ctx, provider.ApplyRequest{Spec: spec}); err == nil {
		t.Fatal("pulled a private image without a credential")
	} else if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), auth) {
		t.Fatalf("error carries the credential: %v", err)
	}

	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "bohux-pull-*"))
	spec.PullConfig = pullSource{cfg: config}
	if _, err := p.Apply(ctx, provider.ApplyRequest{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if id := dockerOut(t, "ps", "-q", "--filter", "label=io.bohux.app="+ref.App); id == "" {
		t.Error("the private image is not running")
	}
	if after, _ := filepath.Glob(filepath.Join(os.TempDir(), "bohux-pull-*")); len(after) != len(before) {
		t.Errorf("credential directories left behind: %v", after)
	}
	// The artifacts on disk name the image, never the credential (rule 2, rule 3).
	_ = filepath.WalkDir(p.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), auth) || strings.Contains(string(b), password) {
			t.Errorf("%s holds the credential", path)
		}
		return nil
	})
}
