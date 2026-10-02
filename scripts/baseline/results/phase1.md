# Baseline `phase1`

- binary: `/tmp/codebot-phase1`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 5/5
- total cost: $0.1714
- total input+output tokens: 1412253

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.6 | 4 | 4 | 6 | 0 | 54933 | 1049 | 37504 | 0.0067 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 15.1 | 6 | 6 | 7 | 0 | 81039 | 2099 | 65408 | 0.0076 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 12.5 | 7 | 7 | 9 | 0 | 101696 | 1826 | 86272 | 0.0073 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.440s |
| rename | True | 18.1 | 9 | 9 | 33 | 21 | 169943 | 3599 | 146432 | 0.0123 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 426.2 | 42 | 42 | 109 | 6 | 978915 | 17154 | 601344 | 0.1375 | 24 | stop | 249 lines, terms 8/8 |
