# Baseline `phase4`

- binary: `/tmp/codebot-phase4`
- fixture: `7938489`
- model: `deepseek-v4-flash`
- passed: 5/5
- total cost: $0.0450
- total input+output tokens: 414655

| id | ok | seconds | llm_calls | turns | tool_calls | tool_errors | input | output | cache_read | cost_usd | compactions | end_reason | detail |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| qa | True | 7.2 | 4 | 4 | 5 | 0 | 32893 | 966 | 20992 | 0.0049 | 0 | stop | kinds mentioned 9/9, worktree clean=True |
| bugfix | True | 12.9 | 7 | 7 | 7 | 0 | 58174 | 1548 | 48000 | 0.0052 | 0 | stop | go test rc=0, test files touched=False |
| feature | True | 20.5 | 8 | 8 | 7 | 0 | 70733 | 3666 | 62592 | 0.0072 | 0 | stop | hidden test rc=0: ok github.com/voocel/codebot/internal/storage 0.442s |
| rename | True | 19.2 | 10 | 10 | 21 | 9 | 150227 | 2672 | 130688 | 0.0099 | 0 | stop | build rc=0, old refs=0, new refs=11 |
| longdoc | True | 39.8 | 7 | 7 | 17 | 1 | 89087 | 4689 | 49536 | 0.0178 | 2 | stop | 193 lines, terms 7/8 |
