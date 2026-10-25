package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Renderer turns raw agent.log lines into the readable one-line-per-action
// view shown by `nightshift logs --agent` and `run -f`. It is display-only:
// agent.log itself is never rewritten. Feed it whole lines; the caller
// buffers partial ones.
type Renderer struct {
	// Roots are absolute directories that tool paths are made relative to.
	Roots []string
	// Loc is the zone header times are shown in; nil means time.Local.
	Loc *time.Location

	iter int
}

var agentHeaderRE = regexp.MustCompile(`^=== iter (\d+) \(session (\d+)\) model=(\S*) planning=(true|false) (\S+) ===$`)

// Iter is the iteration of the most recent header line seen, 0 before any.
func (r *Renderer) Iter() int { return r.iter }

// Line renders one raw line into zero or more display lines.
func (r *Renderer) Line(raw string) []string {
	raw = strings.TrimRight(raw, "\r\n")
	if m := agentHeaderRE.FindStringSubmatch(raw); m != nil {
		r.iter, _ = strconv.Atoi(m[1])
		out := fmt.Sprintf("── iter %s (session %s) %s", m[1], m[2], m[3])
		loc := r.Loc
		if loc == nil {
			loc = time.Local
		}
		if t, err := time.Parse(time.RFC3339, m[5]); err == nil {
			out += " " + t.In(loc).Format("15:04")
		}
		if m[4] == "true" {
			out += " · planning"
		}
		return []string{out + " ──"}
	}
	var out []string
	for _, l := range r.body(raw) {
		out = append(out, r.prefix()+l)
	}
	return out
}

func (r *Renderer) prefix() string {
	if r.iter == 0 {
		return ""
	}
	return fmt.Sprintf("iter %d · ", r.iter)
}

func (r *Renderer) body(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	var ev map[string]any
	if !strings.HasPrefix(trimmed, "{") || json.Unmarshal([]byte(trimmed), &ev) != nil {
		if trimmed == "" {
			return nil
		}
		return []string{cutRunes(raw, 200)}
	}
	typ, _ := ev["type"].(string)
	switch typ {
	case "assistant":
		return r.assistant(ev)
	case "user":
		return userErrors(ev)
	case "result":
		return resultLine(ev)
	case "text":
		if s := firstLineCut(firstStr(dig(ev, "part", "text"), ev["text"]), 160); s != "" {
			return []string{s}
		}
	case "tool_use":
		return []string{r.opencodeTool(ev)}
	case "error":
		return []string{opencodeError(ev)}
	case "":
		if _, ok := ev["result"]; ok {
			return resultLine(ev)
		}
	}
	return nil
}

func (r *Renderer) assistant(ev map[string]any) []string {
	var out []string
	for _, b := range contentBlocks(ev) {
		switch b["type"] {
		case "text":
			if s := firstLineCut(str(b["text"]), 160); s != "" {
				out = append(out, s)
			}
		case "tool_use":
			name := str(b["name"])
			in, _ := b["input"].(map[string]any)
			arg := r.claudeArg(name, in)
			if arg != "" {
				name += " " + arg
			}
			out = append(out, name)
		}
	}
	return out
}

func (r *Renderer) claudeArg(name string, in map[string]any) string {
	switch name {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		p := str(in["file_path"])
		if p == "" {
			p = str(in["path"])
		}
		return r.rel(p)
	case "Bash":
		return firstLineCut(str(in["command"]), 120)
	case "Grep", "Glob":
		return str(in["pattern"])
	case "Task":
		return str(in["description"])
	}
	return ""
}

func (r *Renderer) rel(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	for _, root := range r.Roots {
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return p
}

func userErrors(ev map[string]any) []string {
	var out []string
	for _, b := range contentBlocks(ev) {
		if b["type"] != "tool_result" || b["is_error"] != true {
			continue
		}
		msg := ""
		switch c := b["content"].(type) {
		case string:
			msg = c
		case []any:
			for _, e := range c {
				if m, ok := e.(map[string]any); ok && m["type"] == "text" {
					msg = str(m["text"])
					break
				}
			}
		}
		out = append(out, "  ↳ error: "+firstLineCut(msg, 160))
	}
	return out
}

func resultLine(ev map[string]any) []string {
	if ev["is_error"] == true {
		msg := firstLineCut(str(ev["result"]), 160)
		if msg == "" {
			return []string{"result: error"}
		}
		return []string{"result: error — " + msg}
	}
	var parts []string
	if s := str(ev["subtype"]); s != "" {
		parts = append(parts, s)
	}
	if c, ok := ev["total_cost_usd"].(float64); ok {
		parts = append(parts, fmt.Sprintf("$%.2f", c))
	}
	if n, ok := ev["num_turns"].(float64); ok {
		parts = append(parts, fmt.Sprintf("%d turns", int(n)))
	}
	return []string{strings.TrimSpace("result: " + strings.Join(parts, " · "))}
}

func (r *Renderer) opencodeTool(ev map[string]any) string {
	name := firstStr(ev["tool"], dig(ev, "part", "tool"), ev["name"], dig(ev, "part", "name"))
	var in map[string]any
	for _, c := range []any{ev["input"], dig(ev, "part", "input"), dig(ev, "part", "state", "input"), dig(ev, "state", "input")} {
		if m, ok := c.(map[string]any); ok {
			in = m
			break
		}
	}
	arg := ""
	if s := str(in["command"]); s != "" {
		arg = firstLineCut(s, 120)
	} else if s := firstStr(in["filePath"], in["file_path"], in["path"]); s != "" {
		arg = r.rel(s)
	} else {
		arg = firstStr(in["pattern"], in["description"])
	}
	if arg != "" {
		name += " " + arg
	}
	return strings.TrimSpace(name)
}

func opencodeError(ev map[string]any) string {
	msg := firstStr(ev["message"], ev["error"], dig(ev, "error", "message"), dig(ev, "error", "data", "message"))
	if msg == "" {
		return "error"
	}
	return "error: " + firstLineCut(msg, 160)
}

func contentBlocks(ev map[string]any) []map[string]any {
	var out []map[string]any
	if arr, ok := dig(ev, "message", "content").([]any); ok {
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstStr(vs ...any) string {
	for _, v := range vs {
		if s := str(v); s != "" {
			return s
		}
	}
	return ""
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func firstLineCut(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return cutRunes(strings.TrimSpace(s), n)
}
