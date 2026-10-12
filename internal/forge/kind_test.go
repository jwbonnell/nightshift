package forge_test

import (
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
)

func TestKindHelpers(t *testing.T) {
	cases := []struct {
		kind       forge.Kind
		noun       string
		repoNoun   string
		urlSetting string
	}{
		{forge.KindGitLab, "MR", "project", "NIGHTSHIFT_GITLAB_URL"},
		{forge.KindGitHub, "PR", "repo", "NIGHTSHIFT_GITHUB_URL"},
	}
	for _, c := range cases {
		if got := c.kind.Noun(); got != c.noun {
			t.Errorf("%s.Noun() = %q, want %q", c.kind, got, c.noun)
		}
		if got := c.kind.RepoNoun(); got != c.repoNoun {
			t.Errorf("%s.RepoNoun() = %q, want %q", c.kind, got, c.repoNoun)
		}
		if got := c.kind.URLSetting(); got != c.urlSetting {
			t.Errorf("%s.URLSetting() = %q, want %q", c.kind, got, c.urlSetting)
		}
	}
}
