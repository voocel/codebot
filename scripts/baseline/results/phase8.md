# Baseline `phase8`

- binary: `/tmp/codebot-phase8`
- fixture: `7938489`
- model: `deepseek/deepseek-v4-flash`
- passed: 7/7
- total cost: $0.1318
- total input+output tokens: 923079

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 9.9 | 4 | 4 | 8 | 0 | 50305 | 1492 | 31232 | 0.0077 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 9.2 | 5 | 5 | 6 | 0 | 43982 | 930 | 33024 | 0.0046 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 22.8 | 10 | 10 | 10 | 0 | 91563 | 4098 | 82304 | 0.0082 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.437s |
| rename | True | 13.4 | 7 | 7 | 19 | 4 | 83686 | 1910 | 64768 | 0.0084 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 123.1 | 17 | 17 | 49 | 0 | 348421 | 35659 | 214912 | 0.0841 | 6 | done | 334 lines, terms 8/8 |
| delegate | True | 7.6 | 2 | 2 | 1 | 0 | 10751 | 227 | 5248 | 0.002 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 30.3 | 11 | 11 | 20 | 0 | 245066 | 4989 | 213120 | 0.0168 | 0 | done | answer 2770 chars, worktree clean=True |
