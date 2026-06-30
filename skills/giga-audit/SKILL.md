---
name: giga-audit
description: >
  Review implementation plans and codebases using multi-perspective expert critique and workspace verification.
---

# Giga-Audit Pipeline

Use this skill to review implementation plans, architectures, or existing codebases to find bugs, security flaws, performance bottlenecks, and edge cases.

## Phase 1: Reviewer Personas
Brainstorm 3-4 expert reviewer personas critical to the target domain (e.g., Security Lead, Performance Engineer, QA Tester, Rollback Specialist).

## Phase 2: Critical Questioning
Generate 3 risk-focused questions or concerns per persona based on the proposed plan or codebase.

## Phase 3: Workspace Verification
Investigate the codebase to check if the generated risks exist:
1. Target search queries to narrow down files.
2. If `grep_search` errors out, fall back to native terminal tools (e.g., PowerShell `Select-String -Path ... -Pattern ...` or CMD `findstr`) via `run_command`.
3. Use `view_file` with explicit `StartLine` and `EndLine` parameters to read only relevant code segments. Do not load large files entirely.

## Phase 4: Audit Synthesis
Compile a Markdown audit report directly to an artifact file. Use this structure:
- **Identified Issues**: Grouped by severity (High, Medium, Low) or component.
- **Evidence**: Clickable file links and specific code snippets/line ranges.
- **Recommended Mitigations**: Concrete, actionable steps to resolve each issue.
