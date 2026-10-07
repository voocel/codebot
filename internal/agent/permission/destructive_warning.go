package permission

import "regexp"

// A destructive pattern adds a warning to the approval card and keeps the
// command from being remembered; it never denies. RE2 has no lookahead, so
// dry runs such as git clean -nf warn too: a false warning is acceptable, a
// missed one is not.
type destructivePattern struct {
	re      *regexp.Regexp
	warning string
}

var destructivePatterns = []destructivePattern{
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "may discard uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+push\b[^;&|\n]*[ \t](?:--force|--force-with-lease|-f)\b`), "may overwrite remote history"},
	{regexp.MustCompile(`\bgit\s+clean\b[^;&|\n]*-[a-zA-Z]*f`), "may permanently delete untracked files"},
	{regexp.MustCompile(`\bgit\s+checkout\s+(?:--\s+)?\.[ \t]*(?:$|[;&|\n])`), "may discard working tree changes"},
	{regexp.MustCompile(`\bgit\s+restore\s+(?:--\s+)?\.[ \t]*(?:$|[;&|\n])`), "may discard working tree changes"},
	{regexp.MustCompile(`\bgit\s+stash[ \t]+(?:drop|clear)\b`), "may permanently remove stashed changes"},
	{regexp.MustCompile(`\bgit\s+branch\s+(?:-D[ \t]|--delete\s+--force|--force\s+--delete)\b`), "may force-delete a branch"},

	{regexp.MustCompile(`\bgit\s+(?:commit|push|merge)\b[^;&|\n]*--no-verify\b`), "may skip safety hooks"},
	{regexp.MustCompile(`\bgit\s+commit\b[^;&|\n]*--amend\b`), "may rewrite the last commit"},

	// Ordered -rf, -r, -f so the most specific warning wins.
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR][a-zA-Z]*f|(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]`), "may recursively force-remove files"},
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR]`), "may recursively remove files"},
	{regexp.MustCompile(`(?:^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f`), "may force-remove files"},

	{regexp.MustCompile(`(?:^|[;&|\n]\s*)sudo\b`), "runs with elevated privileges"},

	{regexp.MustCompile(`(?i)\b(?:DROP|TRUNCATE)\s+(?:TABLE|DATABASE|SCHEMA)\b`), "may drop or truncate database objects"},
	{regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+\w+[ \t]*(?:;|"|'|\n|$)`), "may delete all rows from a database table"},

	{regexp.MustCompile(`\bkubectl\s+delete\b`), "may delete Kubernetes resources"},
	{regexp.MustCompile(`\bterraform\s+destroy\b`), "may destroy Terraform infrastructure"},
}

// destructiveCommandWarning returns the first match, so patterns go from most
// specific to least.
func destructiveCommandWarning(cmd string) string {
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
