# Baseline `phase2`

- binary: `/tmp/codebot-phase2`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 4/5
- total cost: $0.0536
- total input+output tokens: 469022

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 6.7 | 4 | 4 | 6 | 0 | 40931 | 1261 | 24704 | 0.0065 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 10.8 | 6 | 6 | 7 | 0 | 56114 | 1628 | 44672 | 0.0057 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 15.1 | 7 | 7 | 7 | 0 | 84095 | 2687 | 70528 | 0.0077 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.451s |
| rename | True | 21.4 | 11 | 11 | 27 | 10 | 175636 | 3785 | 154624 | 0.0118 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | False | 56.6 | 7 | 7 | 20 | 0 | 100908 | 1977 | 36608 | 0.0219 | 4 | stop | docs/permissions.md missing |
