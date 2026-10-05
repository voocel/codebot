package mcp

import (
	"os"
	"regexp"
)

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnvStr expands ${VAR} references in a string from OS environment.
func expandEnvStr(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarRe.FindStringSubmatch(match)[1]
		return os.Getenv(name)
	})
}

// expandEnv converts a map of env vars to KEY=VALUE format,
// expanding ${VAR} references from the OS environment.
// The result is appended to the current process environment.
func expandEnv(env map[string]string) []string {
	base := os.Environ()
	for k, v := range env {
		base = append(base, k+"="+expandEnvStr(v))
	}
	return base
}

// expandHeaders expands ${VAR} in header values.
func expandHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = expandEnvStr(v)
	}
	return out
}
