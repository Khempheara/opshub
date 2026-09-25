package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobEnvSecretsWin(t *testing.T) {
	env := jobEnv(map[string]string{"A": "var", "B": "b", "OPSHUB_WORKSPACE": "/elsewhere"}, map[string]string{"A": "secret"})
	assert.Equal(t, []string{"A=secret", "B=b", "OPSHUB_WORKSPACE=" + Workspace}, env)
}
