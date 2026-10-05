package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultBaseWithOriginHEAD(t *testing.T) {
	upstream := newSourceRepo(t)
	clone := cloneRepo(t, upstream)

	// Advance the clone's local main ahead of origin/main.
	if err := os.WriteFile(filepath.Join(clone, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	mustGit(t, clone, "add", "-A")
	mustGit(t, clone, "commit", "-q", "-m", "local only")

	if got := defaultBaseName(clone); got != "main" {
		t.Fatalf("defaultBaseName = %q, want %q", got, "main")
	}

	originMainSHA := mustGit(t, clone, "rev-parse", "refs/remotes/origin/main")
	localMainSHA := mustGit(t, clone, "rev-parse", "refs/heads/main")
	if originMainSHA == localMainSHA {
		t.Fatalf("test setup: origin/main and local main should differ")
	}

	got, err := resolveBase(clone, "main")
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if got != originMainSHA {
		t.Fatalf("resolveBase(main) = %q, want origin/main %q", got, originMainSHA)
	}
}

func TestResolveBasePreference(t *testing.T) {
	upstream := newSourceRepo(t)
	clone := cloneRepo(t, upstream)
	if err := os.WriteFile(filepath.Join(clone, "extra.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	mustGit(t, clone, "add", "-A")
	mustGit(t, clone, "commit", "-q", "-m", "local only")

	originMainSHA := mustGit(t, clone, "rev-parse", "refs/remotes/origin/main")
	localMainSHA := mustGit(t, clone, "rev-parse", "refs/heads/main")
	if originMainSHA == localMainSHA {
		t.Fatalf("test setup: origin/main and local main should differ")
	}

	if got, err := resolveBase(clone, "main"); err != nil || got != originMainSHA {
		t.Fatalf("resolveBase(main) = (%q, %v), want (%q, nil)", got, err, originMainSHA)
	}
	if got, err := resolveBase(clone, "refs/heads/main"); err != nil || got != localMainSHA {
		t.Fatalf("resolveBase(refs/heads/main) = (%q, %v), want (%q, nil)", got, err, localMainSHA)
	}
	if got, err := resolveBase(clone, localMainSHA); err != nil || got != localMainSHA {
		t.Fatalf("resolveBase(sha) = (%q, %v), want (%q, nil)", got, err, localMainSHA)
	}

	_, err := resolveBase(clone, "nope")
	wantErr := fmt.Sprintf(`base "nope" not found in %s (looked for origin/nope and nope); fetch it or fix base:`, clone)
	if err == nil || err.Error() != wantErr {
		t.Fatalf("err = %v, want %q", err, wantErr)
	}
}

func TestBaseOnlyOnOrigin(t *testing.T) {
	upstream := newSourceRepo(t)
	mustGit(t, upstream, "branch", "feat/y")
	clone := cloneRepo(t, upstream)

	if refExists(clone, "refs/heads/feat/y") {
		t.Fatalf("test setup: feat/y should not exist as a local branch in the clone")
	}
	want := mustGit(t, clone, "rev-parse", "refs/remotes/origin/feat/y")

	got, err := resolveBase(clone, "feat/y")
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if got != want {
		t.Fatalf("resolveBase = %q, want %q", got, want)
	}
}

func TestBaseFromFrontmatterLocalBranch(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-q", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write x.txt: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "feat/x tip")
	wantSHA := mustGit(t, repo, "rev-parse", "HEAD")
	mustGit(t, repo, "checkout", "-q", "main")

	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "base: feat/x\n", repo)

	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.Base != "feat/x" {
		t.Fatalf("Base = %q, want %q", sp.Base, "feat/x")
	}

	gotSHA, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if gotSHA != wantSHA {
		t.Fatalf("resolveBase(%s) = %q, want %q", sp.Base, gotSHA, wantSHA)
	}
}
