# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

No application code yet. Repo contains only `LICENSE` (GPL-3.0) and GitHub Spec Kit (v1.2.3) scaffolding. No build, lint, or test commands exist — add them here once a stack is chosen.

## Handling change requests (mandatory)

Whenever a change is requested, first ask the user whether to use Spec Kit or proceed without it.

**With Spec Kit:**
- Not on a feature branch → create `feature/<branch-name>`, where `<branch-name>` is the name Spec Kit generates.
- Already on a `feature/*` branch and the change fits that feature's scope → stay on it; name the new spec with the current feature name as its root (e.g. on `feature/001-auth`, a new spec is `001-auth-<suffix>`).
- Already on a `feature/*` branch but the change is outside its scope → ask the user whether to continue on the current branch or open a new feature branch.

**Without Spec Kit:** enter plan mode before making changes.

**After every applied change**, update the docs:
- `README.md` — create it if missing
- `CLAUDE.md` — keep commands/architecture current
- `CHANGELOG.md` — create it if missing; do NOT log changes that only touch `README.md` or `CLAUDE.md`

## Workflow: Spec-Driven Development (Spec Kit)

Features are built through the Spec Kit skills in `.claude/skills/`, in this order (defined in `.specify/workflows/speckit/workflow.yml`):

1. `/speckit-constitution` — fill in `.specify/memory/constitution.md` (currently an unfilled template; do this first so later steps have project principles to check against)
2. `/speckit-specify <description>` — creates a sequentially numbered feature directory with `spec.md`
3. `/speckit-clarify` (optional) — resolve underspecified areas in the spec
4. `/speckit-plan` — produces `plan.md` and design artifacts
5. `/speckit-tasks` — produces dependency-ordered `tasks.md`
6. `/speckit-analyze` (optional) — cross-artifact consistency check
7. `/speckit-implement` — executes `tasks.md`; `/speckit-converge` appends tasks for any remaining unbuilt work

Notes:
- Helper scripts run via Python (`"script": "py"` in `.specify/init-options.json`); PowerShell equivalents live alongside in `.specify/scripts/powershell/`.
- The active feature directory is resolved from `SPECIFY_FEATURE_DIRECTORY` env var or `.specify/feature.json` (`feature_directory` key).
- Templates for spec/plan/tasks/checklist are in `.specify/templates/`.

## Git

Work happens on `develop`; `main` is the PR target.
