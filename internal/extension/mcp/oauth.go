package mcp

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/mcp-sdk-go/auth"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"

	"github.com/voocel/codebot/internal/infra/config"
)

// usesOAuth reports whether codebot authorizes cfg by OAuth: an HTTP server
// the user gave no Authorization header of their own.
func usesOAuth(cfg config.MCPServer) bool {
	if cfg.Type != "http" {
		return false
	}
	for k := range cfg.Headers {
		if strings.EqualFold(k, "Authorization") {
			return false
		}
	}
	return true
}

// needsLogin reports whether err is a server refusing its OAuth token, or
// the lack of one.
func needsLogin(err error) bool {
	var se *streamhttp.StatusError
	if !errors.As(err, &se) {
		return false
	}
	if se.StatusCode == http.StatusUnauthorized {
		return true
	}
	c, _ := auth.ParseChallenge(se.Header)
	return se.StatusCode == http.StatusForbidden && c.Error == "insufficient_scope"
}

// tokenStore keeps OAuth tokens by server URL in one file only the user can
// read. Writes hold the file's lock and reads see whole files, so codebot
// processes share what one of them refreshed.
type tokenStore struct{ path string }

func oauthPath() string { return filepath.Join(config.UserConfigDir(), "mcp-oauth.json") }

func (s tokenStore) Token(server string) (*auth.Token, error) {
	tokens, err := s.read()
	return tokens[server], err
}

func (s tokenStore) SaveToken(server string, t *auth.Token) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	unlock, err := config.LockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()
	tokens, err := s.read()
	if err != nil {
		return err
	}
	if t == nil {
		delete(tokens, server)
	} else {
		if tokens == nil {
			tokens = map[string]*auth.Token{}
		}
		tokens[server] = t
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, data, 0o600)
}

func (s tokenStore) read() (map[string]*auth.Token, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tokens map[string]*auth.Token
	return tokens, json.Unmarshal(data, &tokens)
}
