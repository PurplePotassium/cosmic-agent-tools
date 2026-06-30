---
name: giga-research
description: >
  Conduct multi-perspective deep research on a topic, gathering facts and creating a structured, cited report (inspired by Stanford STORM).
---

# Giga-Research (STORM) Pipeline

Use this skill to perform deep, multi-perspective research and compile a high-quality, fully cited report.

## Phase 1: Persona Generation
Brainstorm 3-5 distinct expert personas relevant to the target topic.

## Phase 2: Question Formulation
Generate 3-5 technical questions per persona. Consolidate and deduplicate questions across personas to minimize redundant web queries.

## Phase 3: Information Retrieval
Gather information efficiently:
1. Use a `research` subagent if available to offload web searches and compile concise summaries, preventing main context bloat.
2. Keep queries targeted. If running searches directly, budget queries (cap at 6-8 key searches).
3. Record all source URLs and key facts.

## Phase 4: Outline Curation
Organize findings into a hierarchical Markdown outline.

## Phase 5: Synthesis & Drafting
Draft the report directly to a user-facing artifact. Every claim must end with a clean inline citation linking back to the source URL (e.g. `[Site Name](URL)`). Do not synthesize unsupported claims.

## Phase 6: Bibliography & Verification
1. Cross-check citations to ensure claims match source facts.
2. Append a numbered bibliography at the end of the report linking to all retrieved sources.
