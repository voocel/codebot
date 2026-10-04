# Baseline `phase7`

- binary: `/tmp/codebot-phase7`
- fixture: `7938489`
- model: `deepseek/deepseek-v4-flash`
- passed: 7/7
- total cost: $0.0917
- total input+output tokens: 774950

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.6 | 4 | 4 | 5 | 0 | 35669 | 995 | 23424 | 0.005 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 11.6 | 5 | 5 | 6 | 0 | 45484 | 1716 | 34688 | 0.0055 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 19.0 | 9 | 9 | 11 | 0 | 81183 | 3078 | 71808 | 0.0069 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.418s |
| rename | True | 10.6 | 7 | 7 | 9 | 1 | 50086 | 1121 | 42112 | 0.004 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 181.5 | 19 | 19 | 39 | 2 | 328154 | 11413 | 204800 | 0.0519 | 7 | done | 264 lines, terms 8/8 |
| delegate | True | 7.3 | 2 | 2 | 1 | 0 | 10670 | 351 | 5248 | 0.0021 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 26.9 | 9 | 9 | 18 | 0 | 199743 | 5287 | 170112 | 0.0163 | 0 | done | answer 2826 chars, worktree clean=True |
