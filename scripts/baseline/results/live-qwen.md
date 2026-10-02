# Baseline `live-qwen`

- binary: `/tmp/codebot-live`
- fixture: `7938489`
- model: `qwen/qwen3.8-max`
- passed: 7/7
- total cost: $1.1393
- total input+output tokens: 1582467

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 50.3 | 6 | 6 | 8 | 0 | 75552 | 1973 | 58112 | 0.0612 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 48.8 | 6 | 6 | 7 | 0 | 56647 | 1683 | 44928 | 0.0448 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 174.4 | 11 | 11 | 16 | 0 | 156810 | 8119 | 134144 | 0.1276 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.434s |
| rename | True | 56.5 | 6 | 6 | 15 | 0 | 99837 | 2541 | 77056 | 0.0801 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 1198.1 | 19 | 0 | 0 | 0 | 322725 | 15590 | 193536 | 0.4003 | 7 |  | 228 lines, terms 8/8 |
| delegate | True | 67.8 | 3 | 3 | 2 | 0 | 18196 | 546 | 14464 | 0.0144 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 437.7 | 19 | 19 | 32 | 1 | 808973 | 13275 | 735232 | 0.4109 | 0 | done | answer 6451 chars, worktree clean=True |
