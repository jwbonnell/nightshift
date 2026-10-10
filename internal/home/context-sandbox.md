# Sandbox facts (edit as you learn more)

- Verify runs in a freshly created sbx sandbox from the `claude` template, mounting only the run's clone.
- HOME is /home/agent. There is no global git config and no git identity — tests that commit must set one.
- Assume UTC. Tests must not depend on the host's local time zone (build times with time.Local or fixed zones).
- Network: only sbx's baseline allow-list (Anthropic API, Go module proxy/sumdb, common registries) plus the spec's `egress:` hosts.
- No Docker inside verify — tests that need dockertest or containers cannot run there; exclude them from verify.
- The clone has no remotes; the agent cannot push. Nothing outside the clone is visible.
- Check the Go version in the claude template before relying on a newer `go` directive.
