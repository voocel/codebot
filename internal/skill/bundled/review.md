---
name: review
description: Review changes for bugs, regressions, risks, and missing tests.
when_to_use: Use when the user asks for code review, patch review, regression analysis, or a merge-readiness check.
argument-hint: "[scope]"
arguments:
  - scope
---
Perform a code review focused on correctness, regressions, security risks, and missing test coverage.

If the user supplied a scope, review that scope first:
$ARGUMENTS

Review process:
1. Inspect the relevant files or diff before commenting.
2. Prioritize concrete findings over summaries.
3. Explain the impact of each finding and cite exact files or lines when possible.
4. Call out missing or weak tests when behavior changed.
5. If no issues are found, state that explicitly and mention any residual risks or areas you could not verify.

Return findings first. Keep the final summary brief.
