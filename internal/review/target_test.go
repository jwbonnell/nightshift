package review

import (
	"errors"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
)

func gitlabSettings(url string) Settings {
	return Settings{GitLabURL: url, GitLabToken: "gl-tok"}
}

func githubSettings(url string) Settings {
	if url == "" {
		url = "https://github.com"
	}
	return Settings{GitHubURL: url, GitHubToken: "gh-tok"}
}

func bothSettings(gitlabURL, githubURL string) Settings {
	s := gitlabSettings(gitlabURL)
	gh := githubSettings(githubURL)
	s.GitHubURL, s.GitHubToken = gh.GitHubURL, gh.GitHubToken
	return s
}

func TestParseMRURLGood(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	tgt, err := ParseArgs([]string{"https://gitlab.example.com/team/app/-/merge_requests/12"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	want := Target{Forge: forge.KindGitLab, Repo: "team/app", Number: 12}
	if tgt != want {
		t.Errorf("Target = %+v, want %+v", tgt, want)
	}
}

func TestParseMRURLTrailingPath(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	tgt, err := ParseArgs([]string{"https://gitlab.example.com/team/app/-/merge_requests/12/diffs"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if tgt.Repo != "team/app" || tgt.Number != 12 {
		t.Errorf("Target = %+v", tgt)
	}
}

func TestParseMRURLQueryAndFragment(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	tgt, err := ParseArgs([]string{"https://gitlab.example.com/team/app/-/merge_requests/12?tab=notes#note_1"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if tgt.Repo != "team/app" || tgt.Number != 12 {
		t.Errorf("Target = %+v", tgt)
	}
}

func TestParseMRURLHostMismatch(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	_, err := ParseArgs([]string{"https://evil.example.net/team/app/-/merge_requests/12"}, "", s)
	if err == nil || !strings.Contains(err.Error(), "doesn't match a configured forge") {
		t.Fatalf("err = %v, want host-mismatch error", err)
	}
}

func TestParseMRURLMissingMergeRequests(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	_, err := ParseArgs([]string{"https://gitlab.example.com/team/app/issues/3"}, "", s)
	if err == nil || !strings.Contains(err.Error(), "not an MR URL") {
		t.Fatalf("err = %v, want 'not an MR URL'", err)
	}
}

func TestParseMRURLSubpathBase(t *testing.T) {
	s := gitlabSettings("https://example.com/gitlab")
	tgt, err := ParseArgs([]string{"https://example.com/gitlab/team/app/-/merge_requests/12"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if tgt.Repo != "team/app" || tgt.Number != 12 {
		t.Errorf("Target = %+v", tgt)
	}
}

func TestParseArgsURL(t *testing.T) {
	s := githubSettings("")
	tgt, err := ParseArgs([]string{"https://github.com/owner/repo/pull/7"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	want := Target{Forge: forge.KindGitHub, Repo: "owner/repo", Number: 7}
	if tgt != want {
		t.Errorf("Target = %+v, want %+v", tgt, want)
	}
}

func TestParseArgsPathAndIID(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	tgt, err := ParseArgs([]string{"team/app", "12"}, "", s)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	want := Target{Forge: forge.KindGitLab, Repo: "team/app", Number: 12}
	if tgt != want {
		t.Errorf("Target = %+v, want %+v", tgt, want)
	}
}

func TestParseArgsBadIID(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	if _, err := ParseArgs([]string{"team/app", "abc"}, "", s); err == nil {
		t.Fatal("want error for a non-numeric iid")
	}
	if _, err := ParseArgs([]string{"team/app!abc"}, "", s); err == nil {
		t.Fatal("want error for a non-numeric iid shorthand")
	}
}

func TestParseArgsWrongCount(t *testing.T) {
	s := gitlabSettings("https://gitlab.example.com")
	for _, args := range [][]string{{}, {"a", "b", "c"}} {
		_, err := ParseArgs(args, "", s)
		if err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("ParseArgs(%v): err = %v, want usage error", args, err)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		path, suffix string
		n            int
		want         string
	}{
		{"team/app", "mr", 12, "team-app-mr12"},
		{"Team/App.go", "mr", 3, "Team-App.go-mr3"},
		{"o/r", "pr", 4, "o-r-pr4"},
	}
	for _, c := range cases {
		if got := Slug(c.path, c.n, c.suffix); got != c.want {
			t.Errorf("Slug(%q, %d, %q) = %q, want %q", c.path, c.n, c.suffix, got, c.want)
		}
	}
}

func TestParseTargets(t *testing.T) {
	gitlabErr := errors.New("boom: gitlab token file unreadable")

	cases := []struct {
		name      string
		args      []string
		forgeFlag string
		settings  Settings
		want      Target
		errSubstr string
	}{
		{
			name:     "gitlab URL",
			args:     []string{"https://gitlab.example.com/team/app/-/merge_requests/12"},
			settings: gitlabSettings("https://gitlab.example.com"),
			want:     Target{Forge: forge.KindGitLab, Repo: "team/app", Number: 12},
		},
		{
			name:     "github URL with trailing /files",
			args:     []string{"https://github.com/owner/repo/pull/12/files"},
			settings: githubSettings(""),
			want:     Target{Forge: forge.KindGitHub, Repo: "owner/repo", Number: 12},
		},
		{
			name:     "github enterprise base path",
			args:     []string{"https://ghe.example.com/base/owner/repo/pull/5"},
			settings: githubSettings("https://ghe.example.com/base"),
			want:     Target{Forge: forge.KindGitHub, Repo: "owner/repo", Number: 5},
		},
		{
			name:     "gitlab shorthand",
			args:     []string{"g/p!3"},
			settings: gitlabSettings("https://gitlab.example.com"),
			want:     Target{Forge: forge.KindGitLab, Repo: "g/p", Number: 3},
		},
		{
			name:     "github shorthand",
			args:     []string{"o/r#4"},
			settings: githubSettings(""),
			want:     Target{Forge: forge.KindGitHub, Repo: "o/r", Number: 4},
		},
		{
			name:     "repo n one forge configured",
			args:     []string{"team/app", "9"},
			settings: gitlabSettings("https://gitlab.example.com"),
			want:     Target{Forge: forge.KindGitLab, Repo: "team/app", Number: 9},
		},
		{
			name:      "repo n both forges configured is ambiguous",
			args:      []string{"team/app", "9"},
			settings:  bothSettings("https://gitlab.example.com", ""),
			errSubstr: "ambiguous",
		},
		{
			name:      "repo n with --forge github",
			args:      []string{"owner/repo", "9"},
			forgeFlag: "github",
			settings:  bothSettings("https://gitlab.example.com", ""),
			want:      Target{Forge: forge.KindGitHub, Repo: "owner/repo", Number: 9},
		},
		{
			name:      "unknown host",
			args:      []string{"https://unrelated.example.org/a/b/-/merge_requests/1"},
			settings:  gitlabSettings("https://gitlab.example.com"),
			errSubstr: "doesn't match a configured forge",
		},
		{
			name:      "unconfigured forge shorthand",
			args:      []string{"o/r#4"},
			settings:  Settings{}, // github not configured, no default URL set here
			errSubstr: "github is not configured",
		},
		{
			name:      "unconfigured forge surfaces its token error",
			args:      []string{"g/p!3"},
			settings:  Settings{GitLabTokenErr: gitlabErr},
			errSubstr: "boom: gitlab token file unreadable",
		},
		{
			name:      "forge flag with URL is rejected",
			args:      []string{"https://gitlab.example.com/team/app/-/merge_requests/12"},
			forgeFlag: "gitlab",
			settings:  gitlabSettings("https://gitlab.example.com"),
			errSubstr: "--forge only applies to <repo> <n>",
		},
		{
			name:      "bad forge flag value",
			args:      []string{"team/app", "9"},
			forgeFlag: "bitbucket",
			settings:  gitlabSettings("https://gitlab.example.com"),
			errSubstr: "want gitlab or github",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArgs(c.args, c.forgeFlag, c.settings)
			if c.errSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), c.errSubstr) {
					t.Fatalf("err = %v, want substring %q", err, c.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseArgs: %v", err)
			}
			if got != c.want {
				t.Errorf("Target = %+v, want %+v", got, c.want)
			}
		})
	}
}
