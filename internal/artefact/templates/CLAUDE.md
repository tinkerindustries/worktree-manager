# CLAUDE.md — the artefact template directory

This directory holds the templates the phase-7 onboarding skill renders
into an adopted repository: the `SessionStart` tripwire and the opt-in
`PreToolUse` guard hook, the reference doc, and the `CLAUDE.md` tripwire.
`internal/artefact` embeds these files and substitutes nothing but the
managed block. Creating and removing a worktree are not rendered here:
Claude Code's `WorktreeCreate` and `WorktreeRemove` hooks are registered
once per machine by `wt claude install` and answer for every repository
(`internal/claudehook`).

Rules that bind this directory and everything rendered from it:

- **Templates are inputs, never edited in place.** The rendered artefacts
  live in the adopted repository and are committed there. A change to a
  template is a change to what the skill generates; the acceptance tests
  pin the rendered shape.
- **The body is stable; the facts live in the managed block.** A template's
  body is the instruction text, written only when the file does not exist
  yet. Regeneration replaces only the managed block — the
  `# wt-field:` records and the fact lines — so hand edits to a rendered
  skill survive (plan.md §5 phase 7). A rendered file must therefore read
  anything machine-maintained from its own block at runtime, never from a
  constant baked into the body.
- **The markers are the .env block's own.** Every rendered file closes
  with `# --- managed by wt; edits below are overwritten ---` ... `# ---
  end ---`, and records carry the `# wt-field: name=value` prefix. There
  is exactly one marker convention in the system (05-delivery.md §3.1).
- **No GOOS branch here.** `internal/platform` is the only package in the
  repository permitted to branch on `GOOS`, and a rendered artefact may
  not either: the generated hooks and skills run on whatever machine the
  session runs on, using only POSIX-sh and the repository's own tools.
- **No inference in the artefact.** The rendered text states facts from
  the spec and the ledger; whether a fact means a production stack or a
  resource worth isolating is the onboarding skill's judgement.
