package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "opencode", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(b)
}

func TestOpencodeArgv(t *testing.T) {
	a := opencodeAgent{bin: "opencode"}

	code := a.Argv(Invocation{Role: RoleCode, Model: "openai/gpt-5"})
	wantCode := []string{"env", "OPENCODE_PERMISSION=" + codePermission, "opencode", "run", "--format", "json", "--auto", "-m", "openai/gpt-5"}
	if !equalStrings(code, wantCode) {
		t.Errorf("code Argv = %q, want %q", code, wantCode)
	}
	for _, arg := range code {
		if arg == "openai/gpt-5-prompt" {
			t.Errorf("Argv must never contain the prompt")
		}
	}

	plan := a.Argv(Invocation{Role: RolePlan, Model: "openai/gpt-5"})
	if !equalStrings(plan, wantCode) {
		t.Errorf("plan Argv = %q, want %q (plan and code share permissions)", plan, wantCode)
	}

	review := a.Argv(Invocation{Role: RoleReview, Model: "openai/gpt-5", ReadOnly: true})
	wantReview := []string{"env", "OPENCODE_PERMISSION=" + reviewPermission, "opencode", "run", "--format", "json", "--auto", "-m", "openai/gpt-5"}
	if !equalStrings(review, wantReview) {
		t.Errorf("review Argv = %q, want %q", review, wantReview)
	}

	withCfg := opencodeAgent{bin: "opencode", config: `{"k":"v"}`}
	got := withCfg.Argv(Invocation{Role: RoleCode, Model: "openai/gpt-5"})
	wantCfg := []string{"env", "OPENCODE_PERMISSION=" + codePermission, "OPENCODE_CONFIG_CONTENT={\"k\":\"v\"}", "opencode", "run", "--format", "json", "--auto", "-m", "openai/gpt-5"}
	if !equalStrings(got, wantCfg) {
		t.Errorf("configured Argv = %q, want %q", got, wantCfg)
	}

	noCfg := a.Argv(Invocation{Role: RoleCode, Model: "openai/gpt-5"})
	for _, arg := range noCfg {
		if len(arg) >= len("OPENCODE_CONFIG_CONTENT") && arg[:len("OPENCODE_CONFIG_CONTENT")] == "OPENCODE_CONFIG_CONTENT" {
			t.Errorf("Argv with no config set must omit OPENCODE_CONFIG_CONTENT, got %q", arg)
		}
	}
}

func TestOpencodePermissionsExact(t *testing.T) {
	if codePermission != `{"edit":"allow","bash":{"*":"allow","git push*":"deny","git remote*":"deny","git config*":"deny"},"webfetch":"allow","external_directory":"deny"}` {
		t.Errorf("codePermission = %q", codePermission)
	}
	if reviewPermission != `{"edit":"deny","bash":{"*":"deny"},"webfetch":"deny","websearch":"deny","task":"deny","external_directory":"deny"}` {
		t.Errorf("reviewPermission = %q", reviewPermission)
	}
}

// TestReviewPermissionHasNoFlatDeny guards against regressing to a bare
// "deny" string for bash (or read/glob/grep), which OpenCode's free-tier
// check rejects outright (FreeTierError).
func TestReviewPermissionHasNoFlatDeny(t *testing.T) {
	var perm map[string]any
	if err := json.Unmarshal([]byte(reviewPermission), &perm); err != nil {
		t.Fatalf("reviewPermission is not valid JSON: %v", err)
	}

	for _, key := range []string{"bash", "read", "glob", "grep"} {
		if v, ok := perm[key]; ok {
			if s, isStr := v.(string); isStr && s == "deny" {
				t.Errorf("%s must not be a flat \"deny\": OpenCode's free-tier check rejects that", key)
			}
		}
	}

	bash, ok := perm["bash"].(map[string]any)
	if !ok {
		t.Fatalf("bash = %#v, want a map", perm["bash"])
	}
	if bash["*"] != "deny" {
		t.Errorf("bash[\"*\"] = %v, want \"deny\"", bash["*"])
	}
	for k, v := range bash {
		if v == "allow" {
			t.Errorf("bash[%q] = %q, want no \"allow\" entries", k, v)
		}
	}

	for _, key := range []string{"edit", "webfetch", "websearch", "task"} {
		if perm[key] != "deny" {
			t.Errorf("%s = %v, want \"deny\"", key, perm[key])
		}
	}
}

func TestOpencodeParseFixtures(t *testing.T) {
	a := opencodeAgent{}

	t.Run("success", func(t *testing.T) {
		res := a.Parse(readTestdata(t, "success.jsonl"))
		if !res.Found {
			t.Fatal("expected Found")
		}
		if res.IsError {
			t.Error("expected !IsError")
		}
		wantText := "All done.\nFinal summary line."
		if res.Text != wantText {
			t.Errorf("Text = %q, want %q", res.Text, wantText)
		}
		if res.Message != "All done." {
			t.Errorf("Message = %q, want %q", res.Message, "All done.")
		}
		if !res.CostKnown || res.CostUSD != 0.0123 {
			t.Errorf("CostUSD/CostKnown = %v/%v, want 0.0123/true", res.CostUSD, res.CostKnown)
		}
	})

	t.Run("error429", func(t *testing.T) {
		res := a.Parse(readTestdata(t, "error429.jsonl"))
		if !res.Found || !res.IsError || res.APIStatus != 429 {
			t.Errorf("got %+v", res)
		}
	})

	t.Run("quota", func(t *testing.T) {
		res := a.Parse(readTestdata(t, "quota.jsonl"))
		if !res.Found || !res.IsError || res.APIStatus != 0 {
			t.Errorf("got %+v, want IsError with no numeric status", res)
		}
	})

	t.Run("error503", func(t *testing.T) {
		res := a.Parse(readTestdata(t, "error503.jsonl"))
		if !res.Found || !res.IsError || res.APIStatus != 503 {
			t.Errorf("got %+v", res)
		}
	})

	t.Run("unknown and garbage ignored", func(t *testing.T) {
		res := a.Parse(`{"type":"mystery","part":{"text":"nope"}}` + "\ngarbage line\n")
		if res.Found {
			t.Errorf("got %+v, want Found=false for only-unknown/garbage input", res)
		}
	})
}

func TestOpencodeTransient(t *testing.T) {
	a := opencodeAgent{}

	cases := []struct {
		file string
		want bool
	}{
		{"error429.jsonl", true},
		{"quota.jsonl", true},
		{"error503.jsonl", true},
		{"success.jsonl", false},
		{"stderr429.txt", true},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			res := a.Parse(readTestdata(t, c.file))
			if got := a.Transient(res); got != c.want {
				t.Errorf("Transient(%s) = %v, want %v (res=%+v)", c.file, got, c.want, res)
			}
			if a.RateLimited(res) {
				t.Errorf("RateLimited(%s) = true, want always false for opencode", c.file)
			}
		})
	}
}
