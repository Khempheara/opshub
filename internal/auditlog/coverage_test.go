package auditlog

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionLiterals finds audited actions written as literals in the services:
// `Action: "x.y"`, `recordAudit(ctx, q, "x.y"`, `action = "x.y"` and `string(authz.X)` uses
// resolved through authz's constants.
var (
	literalAction = regexp.MustCompile(`(?:Action: +|recordAudit\(ctx, [a-z]+, |action = |"deployment\." \+ )"?([a-z_0-9]+\.[a-z_0-9]+)"`)
	authzAction   = regexp.MustCompile(`(?:Action: +|action: +|action := |auditEntry\(acc, )string\(authz\.([A-Za-z]+)\)`)
	authzConst    = regexp.MustCompile(`\t([A-Za-z]+) +Action = "([a-z_.]+)"`)
)

// Every action the services record has a sentence, so the log never shows the fallback for
// a known action. Add a sentence to internal/i18n/locales/{en,km}.json with new actions.
func TestEveryRecordedActionHasASentence(t *testing.T) {
	b := bundle(t)
	authzSrc, err := os.ReadFile("../authz/authz.go")
	require.NoError(t, err)
	consts := map[string]string{}
	for _, m := range authzConst.FindAllStringSubmatch(string(authzSrc), -1) {
		consts[m[1]] = m[2]
	}
	actions := map[string]string{
		// Built at run time from the deployment status.
		"deployment.succeeded": "deploy/run.go", "deployment.failed": "deploy/run.go",
		// Recorded by this package.
		"audit.export": "auditlog/export.go",
	}
	require.NoError(t, filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "/store/") || strings.Contains(path, "/auditlog/") {
			return err
		}
		src, err := os.ReadFile(path) // #nosec G304 -- walking this repository's sources
		if err != nil {
			return err
		}
		for _, m := range literalAction.FindAllStringSubmatch(string(src), -1) {
			actions[m[1]] = path
		}
		for _, m := range authzAction.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if a, ok := consts[name]; ok {
				actions[a] = path
			} else {
				t.Errorf("%s: unknown authz constant %s", path, name)
			}
		}
		return nil
	}))
	require.Greater(t, len(actions), 80, "the scan should find the recorded actions")
	for action, path := range actions {
		for _, loc := range []string{"en", "km"} {
			_, ok := b.Lookup(loc, "audit."+action, map[string]any{})
			if !ok { // actions with variants have a base sentence too
				t.Errorf("%s: no %s sentence for %s", path, loc, action)
			}
		}
	}
	assert.Contains(t, actions, "log_token.revoke")
	assert.Contains(t, actions, "member.update_role")
	assert.Contains(t, actions, "member.remove")
	assert.Contains(t, actions, "secret.rotate")
	assert.Contains(t, actions, "auth.login")
}
