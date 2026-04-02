---
name: debug
description: Investigate a bug systematically before proposing or applying a fix.
when_to_use: Use when the task is about a failing behavior, broken test, runtime error, or an unclear bug that needs root-cause analysis.
argument-hint: "[problem]"
arguments:
  - problem
---
Investigate the problem methodically and aim to identify the root cause before patching.

Problem statement:
$ARGUMENTS

Debug process:
1. Reproduce the issue or gather the strongest available evidence.
2. Narrow the failure to the smallest relevant code path.
3. Distinguish symptoms from root cause.
4. If you change code, keep the fix minimal and directly tied to the root cause.
5. Verify the fix with the most relevant test or reproduction step.

Do not hide uncertainty. If reproduction is incomplete, say what is known, what is inferred, and what remains unverified.
