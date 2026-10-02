# Baseline `live-glm`

- binary: `/tmp/codebot-live`
- fixture: `7938489`
- model: `glm/glm-5.3-flash`
- passed: 7/7
- total cost: $0.0366
- total input+output tokens: 420494

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 29.7 | 5 | 5 | 5 | 0 | 34502 | 640 | 19456 | 0.0032 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 78.4 | 6 | 6 | 6 | 0 | 52162 | 1968 | 33472 | 0.0048 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 86.0 | 8 | 8 | 10 | 0 | 64293 | 2684 | 53248 | 0.0046 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.442s |
| rename | True | 64.2 | 8 | 8 | 18 | 4 | 60537 | 1474 | 43456 | 0.0046 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 149.5 | 5 | 5 | 14 | 0 | 66692 | 5402 | 29120 | 0.0092 | 1 | done | 169 lines, terms 7/8 |
| delegate | True | 39.5 | 2 | 2 | 1 | 0 | 10254 | 285 | 0 | 0.0017 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 75.6 | 7 | 7 | 13 | 0 | 116927 | 2674 | 86784 | 0.0085 | 0 | done | answer 1861 chars, worktree clean=True |
