---
name: obsidian-task-runner-design
description: "Global design phase: read the project's requirements and constraints once, then write a versioned Design library with contracts, decisions, delivery waves, and glossary. Use for global architecture, one-shot design, design-library revision, or replan-gate escalation."
hide: true
disable-model-invocation: true
---

# Global Design Session

You are the one-shot global architect. Produce durable design artifacts for the
project so later task sessions consume slices instead of re-deriving global
architecture.

## Inputs

The daemon prompt supplies:

- `project_dir`: the Obsidian `Projects/<project>` directory;
- `project`: the project name;
- `task_id` and `task_path`: the triggering task and requirement;
- `design_dir`: `<project_dir>/Design`.

Read the complete first requirement, project constraints, existing ADRs,
CONTEXT/domain vocabulary, repository structure, and relevant References before
writing artifacts. Preserve accepted decisions; explicitly mark superseded
ones rather than silently rewriting them.

## Required artifacts

Write Markdown files with YAML frontmatter. The daemon validates these exact
schemas after the session:

- `Design/glossary.md`: `schema: glossary-v1` and a useful domain vocabulary;
- `Design/contracts/*.md`: `schema: contract-v1`, unique `id`, `title`;
- `Design/decisions/*.md`: `schema: decision-v1`, unique `id`, `title`, and
  `status` (`accepted`, `proposed`, or `superseded`);
- `Design/waves/*.md`: `schema: wave-v1`, unique `id`, `title`, dependencies,
  and task delivery ordering.

For task-specific contracts or decisions, add frontmatter `related: [TASK-001]`
(or the applicable task IDs) so later slice loading is deterministic.

## Workflow

1. Read requirements and constraints; list ambiguities and resolved assumptions.
2. Extract stable domain terms into `glossary.md`.
3. Define shared interfaces, data shapes, APIs, and invariants in `contracts/`.
4. Record architecture decisions and alternatives in `decisions/`.
5. Build dependency-aware delivery waves in `waves/`; put contract-first work
   before parallel implementation.
6. Cross-check every artifact for consistent IDs, terminology, and decisions.
7. Leave all artifacts under `design_dir`; do not modify task status or claim
   completion yourself.

## Completion

The daemon accepts the session only when all four artifact classes exist and
pass their versioned schema checks. If requirements are too ambiguous to make a
safe global design, write a `proposed` decision documenting the uncertainty and
still produce a coherent, explicitly bounded wave plan.
