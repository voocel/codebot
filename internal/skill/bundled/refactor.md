---
name: refactor
description: Improve code structure while preserving behavior.
when_to_use: Use when the user wants cleaner structure, lower coupling, duplication removal, or an easier-to-extend design without changing intended behavior.
argument-hint: "[scope]"
arguments:
  - scope
---
Refactor the target carefully while preserving externally observable behavior.

Refactor target:
$ARGUMENTS

Refactor process:
1. Identify the specific design or maintainability problem first.
2. Prefer small, composable changes over sweeping rewrites.
3. Preserve public behavior unless the user explicitly asked for behavior changes.
4. Keep naming, boundaries, and data flow easier to understand after the change.
5. Validate the result with focused tests or a concrete reasoning trail if tests are unavailable.

Call out any tradeoff where a cleaner design would require a larger follow-up change.
