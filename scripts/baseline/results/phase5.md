# Baseline `phase5`

- binary: `/tmp/codebot-phase5`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 7/7
- total cost: $0.1142
- total input+output tokens: 955563

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 8.1 | 4 | 4 | 5 | 0 | 37272 | 1115 | 24064 | 0.0054 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 11.4 | 5 | 5 | 6 | 0 | 45973 | 1454 | 34944 | 0.0053 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 13.1 | 6 | 6 | 5 | 0 | 45416 | 2109 | 37632 | 0.0051 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.403s |
| rename | True | 15.5 | 7 | 7 | 16 | 5 | 57671 | 2356 | 48896 | 0.0058 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 220.6 | 28 | 28 | 47 | 9 | 518815 | 19969 | 362240 | 0.0731 | 10 | stop | 253 lines, terms 8/8 |
| delegate | True | 11.7 | 2 | 2 | 1 | 0 | 11813 | 216 | 5504 | 0.0022 | 0 | stop | delegated=True, named=True, worktree clean=True |
| plan | True | 32.2 | 10 | 10 | 18 | 1 | 205340 | 6044 | 175232 | 0.0173 | 0 | stop | answer 2504 chars, worktree clean=True |
