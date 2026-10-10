package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeployFilesByteIdenticalToSpec guards D9: deploy/ must hold the
// three files exactly as SPEC.md specifies them, byte for byte. It
// extracts each fenced block following its "`deploy/<name>`" label from
// SPEC.md and diffs it against the real file, the same mechanical
// approach TestPreamblesByteIdenticalToReference uses for the prompts.
func TestDeployFilesByteIdenticalToSpec(t *testing.T) {
	specData, err := os.ReadFile(filepath.Join("..", "..", "SPEC.md"))
	if err != nil {
		t.Fatalf("read SPEC.md: %v", err)
	}
	spec := string(specData)

	for _, name := range []string{
		"nightshift-nightly.sh",
		"nightshift.service",
		"nightshift.timer",
	} {
		label := "`deploy/" + name + "`"
		i := strings.Index(spec, label)
		if i < 0 {
			t.Fatalf("label %q not found in SPEC.md", label)
		}
		rest := spec[i:]

		fenceStart := strings.Index(rest, "```")
		if fenceStart < 0 {
			t.Fatalf("no fenced block after %q", label)
		}
		rest = rest[fenceStart:]
		bodyStart := strings.Index(rest, "\n") + 1
		bodyEnd := strings.Index(rest[bodyStart:], "```")
		if bodyEnd < 0 {
			t.Fatalf("unterminated fenced block after %q", label)
		}
		want := rest[bodyStart : bodyStart+bodyEnd]

		if name == "nightshift-nightly.sh" {
			// nightshift-run-checks (D11-style sanctioned deviation): the
			// new rate-limited status gets named in this one comment
			// without touching SPEC.md itself. Assert there's exactly one
			// occurrence first, so this substitution still fails loudly if
			// anything else in the block drifts from SPEC.md.
			old := "# Only a runner error (1) marks the unit failed; blocked/red/stalled/capped are outcomes.\n"
			updated := "# Only a runner error (1) marks the unit failed; blocked/red/stalled/capped/rate-limited are outcomes.\n"
			if n := strings.Count(want, old); n != 1 {
				t.Fatalf("expected exactly one occurrence of the old comment line, got %d", n)
			}
			want = strings.Replace(want, old, updated, 1)
		}

		got, err := os.ReadFile(filepath.Join("..", "..", "deploy", name))
		if err != nil {
			t.Fatalf("read deploy/%s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("deploy/%s does not match SPEC.md byte-for-byte\ngot:  %q\nwant: %q", name, got, want)
		}
	}
}
