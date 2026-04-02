# Copilot Instructions — Docs (agentguard-workspace)

## Purpose

This repo is the **AgentGuard workspace root** — it contains strategy documents,
plans, configuration, and scripts. There is no application code here.

## Golden Rules

1. **Never modify application code.** This repo has no Go, Python, TypeScript, or
   other application source. If a script exists in `scripts/`, treat it as
   infrastructure — do not refactor it without explicit instruction.
2. **Never create code files.** Do not add `.go`, `.py`, `.ts`, or similar files.
3. **Documentation only.** All contributions should be Markdown (`.md`), JSON
   config, or shell scripts for workspace orchestration.

## Markdown Formatting

### Structure

- Use ATX-style headers (`#`, `##`, `###`). Never use setext (underline) style.
- One blank line before and after every header.
- One blank line before and after code blocks, tables, and lists.
- Maximum line length: soft limit 100 characters for prose. Tables and URLs may exceed.

### Headers

- `#` — Document title (exactly one per file).
- `##` — Major sections.
- `###` — Subsections.
- `####` — Use sparingly, only for deeply nested content.
- Never skip levels (e.g., `#` followed by `###`).

### Lists

- Use `-` for unordered lists (not `*` or `+`).
- Use `1.` for ordered lists (not auto-incrementing `1.`, `2.`, `3.`).
- Indent nested lists with 2 spaces.

### Code Blocks

- Always specify a language identifier: ` ```bash `, ` ```json `, ` ```yaml `.
- Use inline code (`` ` ``) for file names, CLI commands, variable names, and labels.

### Tables

- Align columns with pipes.
- Use a header separator row (`|---|---|`).
- Keep tables compact — avoid embedding long prose in cells.

### Links

- Prefer relative links for cross-references within the repo.
- Use descriptive link text, not "click here" or bare URLs.

## Strategy Document Conventions

### File Naming

- Plans: `docs/superpowers/plans/YYYY-MM-DD-<slug>.md`
- Specs: `docs/superpowers/specs/YYYY-MM-DD-<slug>.md`
- ADRs: `docs/adr/NNNN-<slug>.md`

### Front Matter

Strategy docs should open with a metadata block:

```markdown
# Title

- **Date:** YYYY-MM-DD
- **Author:** <name>
- **Status:** draft | active | completed | archived
- **Squad:** <squad-name> (if applicable)
```

### Sections

Standard sections for plans and specs:

1. **Summary** — 2-3 sentences, what and why.
2. **Goals** — Bulleted list of measurable outcomes.
3. **Non-Goals** — What this explicitly does NOT cover.
4. **Design / Approach** — How it works.
5. **Phases** — If multi-phase, enumerate with clear milestones.
6. **Open Questions** — Unresolved decisions.
7. **References** — Links to related docs, PRs, issues.

### Tone

- Write for a technical audience (engineers and agent operators).
- Be concise. Prefer short paragraphs (2-4 sentences).
- Use active voice.
- Avoid marketing language, superlatives, and filler.

## Do NOT

- Add application code or library dependencies.
- Modify `.agentguard-root-session` or `.agentguard/` files without explicit instruction.
- Create files outside the established directory structure.
- Use emoji in document titles or headers.
- Add images without confirming the asset is committed (no broken image links).
