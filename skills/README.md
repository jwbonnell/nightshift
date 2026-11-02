# Agent skills

Skills for working with nightshift from Claude Code, Claude Desktop/claude.ai, opencode or
any agent that reads the Agent Skills format (a folder holding a `SKILL.md`).

| Skill | Use it for |
|---|---|
| `nightshift-spec/` | Drafting and fixing specs, turning review findings into follow-up specs, and reviewing finished, red, stalled or blocked runs |

## Install

From a checkout of this repo, link the folder so `git pull` keeps it current:

```sh
# Claude Code
mkdir -p ~/.claude/skills && ln -sfn "$PWD/skills/nightshift-spec" ~/.claude/skills/nightshift-spec

# opencode and other agents that read ~/.agents/skills
mkdir -p ~/.agents/skills && ln -sfn "$PWD/skills/nightshift-spec" ~/.agents/skills/nightshift-spec
```

For Claude Desktop or claude.ai, zip the folder (`cd skills && zip -r nightshift-spec.zip
nightshift-spec`) and upload the zip as a skill in Claude's settings.

The interview that `nightshift new` starts has its own instructions in
`internal/interview/`. The skill follows the same checklist, so keep the two in step when
either changes.
