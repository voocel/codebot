# Baseline `phase5b`

- binary: `/tmp/codebot-phase5b`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 7/7
- total cost: $0.0574
- total input+output tokens: 503892

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 9.2 | 5 | 5 | 6 | 0 | 43035 | 1104 | 30592 | 0.0052 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 10.3 | 5 | 5 | 6 | 0 | 45595 | 1125 | 34432 | 0.0049 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 15.5 | 7 | 7 | 7 | 0 | 55206 | 2730 | 47232 | 0.006 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.420s |
| rename | True | 11.2 | 6 | 6 | 14 | 0 | 50887 | 1571 | 40448 | 0.0053 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 40.5 | 5 | 5 | 15 | 0 | 115174 | 7184 | 81792 | 0.0191 | 1 | stop | 345 lines, terms 8/8 |
| delegate | True | 10.2 | 2 | 2 | 1 | 0 | 11603 | 362 | 5632 | 0.0023 | 0 | stop | delegated=True, named=True, worktree clean=True |
| plan | True | 26.3 | 8 | 8 | 15 | 1 | 163742 | 4574 | 135936 | 0.0146 | 0 | stop | answer 1998 chars, worktree clean=True |
