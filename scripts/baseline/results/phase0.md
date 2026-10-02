# Baseline `phase0`

- binary: `/tmp/codebot-phase0`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 5/5
- total cost: $0.1312
- total input+output tokens: 1277601

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.6 | 4 | 4 | 5 | 0 | 74875 | 1084 | 51712 | 0.0086 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 19.3 | 10 | 10 | 11 | 2 | 180628 | 2431 | 160000 | 0.0101 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 12.6 | 5 | 5 | 7 | 0 | 83887 | 1453 | 66048 | 0.0075 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.398s |
| rename | True | 20.5 | 11 | 11 | 20 | 3 | 297091 | 2004 | 266368 | 0.0132 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 452.8 | 23 | 23 | 74 | 2 | 622610 | 11538 | 370048 | 0.0918 | 15 | stop | 397 lines, terms 8/8 |
