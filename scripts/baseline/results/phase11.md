# Baseline `phase11`

- binary: `/tmp/codebot-phase11`
- fixture: `7938489`
- model: `deepseek/deepseek-v4-flash`
- passed: 8/8
- total cost: $0.1056
- total input+output tokens: 1041944

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.0 | 4 | 4 | 5 | 0 | 34917 | 918 | 22912 | 0.0048 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 13.6 | 7 | 7 | 6 | 0 | 58451 | 1480 | 47744 | 0.0053 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 16.6 | 9 | 9 | 8 | 0 | 70616 | 2531 | 62464 | 0.0059 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.435s |
| rename | True | 15.4 | 9 | 9 | 19 | 4 | 76822 | 2019 | 66432 | 0.0059 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 209.5 | 26 | 26 | 46 | 3 | 470354 | 28302 | 389120 | 0.0607 | 4 | done | 254 lines, terms 8/8 |
| delegate | True | 8.6 | 2 | 2 | 1 | 0 | 10986 | 279 | 5504 | 0.002 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 28.3 | 9 | 9 | 17 | 0 | 188891 | 4937 | 159232 | 0.0158 | 0 | done | answer 3302 chars, worktree clean=True |
| multiturn | True | 14.7 | 11 | 11 | 8 | 0 | 89042 | 1399 | 78976 | 0.0052 | 0 | done | compaction named=True, later turns' first-call cache hit ['0.98', '0.99'], worktree clean=True |
