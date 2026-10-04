# Baseline `phase10`

- binary: `/tmp/codebot-phase10`
- fixture: `7938489`
- model: `deepseek/deepseek-v4-flash`
- passed: 8/8
- total cost: $0.0833
- total input+output tokens: 626646

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.0 | 3 | 3 | 4 | 0 | 23332 | 860 | 11776 | 0.0046 | 0 | done | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 28.3 | 5 | 5 | 6 | 0 | 45146 | 1707 | 34432 | 0.0055 | 0 | done | go test rc=0, test files touched=False |
| feature | True | 17.6 | 7 | 7 | 8 | 0 | 79869 | 2208 | 67584 | 0.0067 | 0 | done | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.422s |
| rename | True | 11.5 | 7 | 7 | 8 | 0 | 48198 | 894 | 40704 | 0.0036 | 0 | done | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 69.3 | 7 | 7 | 22 | 0 | 146138 | 18921 | 86912 | 0.041 | 3 | error | 378 lines, terms 8/8 |
| delegate | True | 11.5 | 2 | 2 | 1 | 0 | 11116 | 303 | 5504 | 0.0021 | 0 | done | delegated=True, named=True, worktree clean=True |
| plan | True | 24.5 | 8 | 8 | 14 | 0 | 158793 | 4285 | 130048 | 0.0145 | 0 | done | answer 2881 chars, worktree clean=True |
| multiturn | True | 19.4 | 9 | 9 | 7 | 0 | 83770 | 1106 | 72064 | 0.0053 | 0 | done | compaction named=True, later turns' first-call cache hit ['0.98', '0.99'], worktree clean=True |
