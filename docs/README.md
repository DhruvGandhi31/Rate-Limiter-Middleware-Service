# docs/

Project notes that don't belong in the top-level README.

| File | What it holds |
|------|---------------|
| [`CHANGELOG.md`](CHANGELOG.md) | Human-readable log of merged changes, newest first. Update on every merge to `master`. |
| [`DECISIONS.md`](DECISIONS.md) | Short design-decision entries — what was chosen, what was rejected, and why. Append-only. |

Suggested future additions (not yet written):

- `API.md` — request/response reference for `/check`, `/status`, `/reset`, `/health`, `/metrics`
- `RUNBOOK.md` — operator playbook: what to do when Redis is slow, when the reject rate spikes, when the pool is exhausted
- `architecture.png` — a rendered diagram of the request flow (the top-level README currently has an ASCII version)
