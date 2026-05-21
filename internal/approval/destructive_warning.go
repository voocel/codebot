package approval

import "regexp"

// Patterns that flag a bash command as potentially destructive. Each match
// produces a UI-only warning on the approval card — the deny/allow decision
// is unaffected. Goal: a final cognitive checkpoint before the user presses Y,
// especially in trust / always-allow modes where the engine would otherwise
// pass silently.
//
// Mirrors claude-code-src/tools/BashTool/destructiveCommandWarning.ts. Go's
// RE2 lacks lookahead, so CC patterns that exclude dry-run forms are
// simplified to warn unconditionally — a false-positive warning on
// `git clean -nf` is acceptable; missing the warning on `git clean -f` is not.
type destructivePattern struct {
	re      *regexp.Regexp
	warning string
}

var destructivePatterns = []destructivePattern{
	// Git — data loss / hard to reverse
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "may discard uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+push\b[^;&|\n]*[ \t](?:--force|--force-with-lease|-f)\b`), "may overwrite remote history"},
	{regexp.MustCompile(`\bgit\s+clean\b[^;&|\n]*-[a-zA-Z]*f`), "may permanently delete untracked files"},
	{regexp.MustCompile(`\bgit\s+checkout\s+(?:--\s+)?\.[ \t]*(?:$|[;&|\n])`), "may discard working tree changes"},
	{regexp.MustCompile(`\bgit\s+restore\s+(?:--\s+)?\.[ \t]*(?:$|[;&|\n])`), "may discard working tree changes"},
	{regexp.MustCompile(`\bgit\s+stash[ \t]+(?:drop|clear)\b`), "may permanently remove stashed changes"},
	{regexp.MustCompile(`\bgit\s+branch\s+(?:-D[ \t]|--delete\s+--force|--force\s+--delete)\b`), "may force-delete a branch"},

	// Git — safety bypass
	{regexp.MustCompile(`\bgit\s+(?:commit|push|merge)\b[^;&|\n]*--no-verify\b`), "may skip safety hooks"},
	{regexp.MustCompile(`\bgit\s+commit\b[^;&|\n]*--amend\b`), "may rewrite the last commit"},

	// File deletion (ordered: -rf > -r > -f so the most specific reason wins)
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR][a-zA-Z]*f|(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]`), "may recursively force-remove files"},
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR]`), "may recursively remove files"},
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f`), "may force-remove files"},

	// Privilege escalation
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)sudo\b`), "runs with elevated privileges"},

	// Database
	{regexp.MustCompile(`(?i)\b(?:DROP|TRUNCATE)\s+(?:TABLE|DATABASE|SCHEMA)\b`), "may drop or truncate database objects"},
	{regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+\w+[ \t]*(?:;|"|'|\n|$)`), "may delete all rows from a database table"},

	// Infrastructure
	{regexp.MustCompile(`\bkubectl\s+delete\b`), "may delete Kubernetes resources"},
	{regexp.MustCompile(`\bterraform\s+destroy\b`), "may destroy Terraform infrastructure"},
}

// DestructiveCommandWarning returns a short warning string when cmd matches
// a known destructive pattern, or "" otherwise. The returned phrase is the
// noun phrase only (e.g. "may overwrite remote history") — the caller adds
// any label / icon / styling. Returns the FIRST match, so order patterns from
// most specific to least.
func DestructiveCommandWarning(cmd string) string {
	if cmd == "" {
		return ""
	}
	for _, p := range destructivePatterns {
		if p.re.MatchString(cmd) {
			return p.warning
		}
	}
	return ""
}
