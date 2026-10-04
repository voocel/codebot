# Baseline `phase9`

- binary: `/tmp/codebot-phase9`
- fixture: `7938489`
- model: `deepseek/deepseek-v4-flash`
- passed: 7/7
- total cost: $0.0989
- total input+output tokens: 883463

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.7 | 4 | 4 | 6 | 0 | 32262 | 1098 | 19328 | 0.0053 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 58.3 | 5 | 5 | 6 | 0 | 44984 | 1488 | 34432 | 0.0052 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 14.8 | 7 | 7 | 9 | 0 | 58075 | 1830 | 48896 | 0.0052 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.433s |
| rename | True | 15.4 | 8 | 8 | 20 | 5 | 69048 | 2568 | 59136 | 0.0064 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 104.2 | 23 | 23 | 40 | 2 | 420887 | 23900 | 331264 | 0.0576 | 4 | done | 305 lines, terms 7/8 |
| delegate | True | 13.4 | 2 | 2 | 1 | 0 | 11157 | 422 | 5376 | 0.0023 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 37.6 | 10 | 10 | 20 | 0 | 209869 | 5875 | 180736 | 0.0169 | 0 | done | answer 2640 chars, worktree clean=True |
