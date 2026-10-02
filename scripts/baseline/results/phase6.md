# Baseline `phase6`

- binary: `/tmp/codebot-phase6`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 7/7
- total cost: $0.1373
- total input+output tokens: 1312034

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 8.0 | 4 | 4 | 7 | 0 | 36248 | 1294 | 22784 | 0.0057 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 15.2 | 6 | 6 | 7 | 0 | 57882 | 1980 | 46208 | 0.0062 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 17.2 | 9 | 9 | 11 | 0 | 72389 | 2519 | 64000 | 0.0059 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.605s |
| rename | True | 13.4 | 8 | 8 | 18 | 4 | 67905 | 1820 | 57600 | 0.0056 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 363.7 | 50 | 50 | 79 | 2 | 879968 | 17297 | 649472 | 0.0938 | 15 | done | 254 lines, terms 8/8 |
| delegate | True | 7.3 | 2 | 2 | 1 | 0 | 10728 | 352 | 5248 | 0.0021 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 33.7 | 7 | 7 | 20 | 0 | 155608 | 6044 | 122368 | 0.018 | 0 | done | answer 3571 chars, worktree clean=True |
