package spec

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The example from the product specification must parse as documented.
const specExample = `version: 1
on: [push, pull_request]
stages: [test, build, deploy]
jobs:
  test:
    stage: test
    image: golang:1.23
    steps:
      - run: go test ./...
  build:
    stage: build
    needs: [test]
    image: docker:27
    steps:
      - run: docker build -t app:${OPSHUB_COMMIT_SHA} .
    artifacts: [dist/]
  deploy-prod:
    stage: deploy
    needs: [build]
    environment: production
    when: manual            # requires approval
    deploy:
      target: k8s-prod
      strategy: rolling
`

func problemsOf(t *testing.T, src string) Problems {
	t.Helper()
	_, err := Parse([]byte(src))
	require.Error(t, err)
	var ps Problems
	require.True(t, errors.As(err, &ps), "got %v", err)
	return ps
}

func rules(ps Problems) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Path+":"+p.Rule)
	}
	return out
}

func TestSpecExample(t *testing.T) {
	def, err := Parse([]byte(specExample))
	require.NoError(t, err)
	assert.Equal(t, []string{"test", "build", "deploy"}, def.Stages)
	assert.NotNil(t, def.Triggers.Push)
	assert.NotNil(t, def.Triggers.PullRequest)
	assert.Nil(t, def.Triggers.Tag)
	require.Len(t, def.Jobs, 3)

	build := def.Jobs[1]
	assert.Equal(t, "build", build.Name)
	assert.Equal(t, []string{"test"}, build.Needs)
	assert.Equal(t, &Artifacts{Paths: []string{"dist/"}, ExpireDays: 7}, build.Artifacts)
	assert.Equal(t, []Step{{Name: "docker build -t app:${OPSHUB_COMMIT_SHA} .", Run: "docker build -t app:${OPSHUB_COMMIT_SHA} ."}}, build.Steps)
	assert.Equal(t, 3600, build.TimeoutSeconds)

	deploy := def.Jobs[2]
	assert.Equal(t, WhenManual, deploy.When)
	assert.Equal(t, "production", deploy.Environment)
	assert.Equal(t, &Deploy{Target: "k8s-prod", Strategy: "rolling", Version: DefaultDeployVersion}, deploy.Deploy)
	assert.Empty(t, deploy.Steps, "a deploy job needs no steps or image")
}

func TestFullSyntax(t *testing.T) {
	def, err := Parse([]byte(`version: 1
on:
  push:
    branches: [main, "release/*"]
    tags: ["v*"]
  pull_request:
    branches: [main]
  schedule:
    - cron: "0 3 * * *"
  manual: true
stages: [check, test, publish]
variables:
  GOFLAGS: -mod=readonly
jobs:
  lint:
    stage: check
    image: golangci/golangci-lint:v2
    steps: [golangci-lint run]
  unit:
    stage: check
    image: golang:1.23
    runs_on: [linux, docker]
    timeout: 20m
    variables: { CGO_ENABLED: "0" }
    cache: { key: "go-${OPSHUB_REF}", paths: [.cache/go] }
    steps:
      - name: Test
        run: |
          go test ./...
          go vet ./...
  integration:
    stage: test
    image: golang:1.23
    steps: [make integration]
  notify:
    stage: publish
    image: alpine:3
    when: always
    needs: []
    steps: [echo done]
  report:
    stage: publish
    image: alpine:3
    when: on_failure
    steps: [echo failed]
    artifacts: { paths: [report.xml], expire_in: 30d }
`))
	require.NoError(t, err)
	tr := def.Triggers
	assert.Equal(t, []string{"main", "release/*"}, tr.Push.Branches)
	assert.Equal(t, []string{"v*"}, tr.Tag.Branches)
	assert.Equal(t, []string{"main"}, tr.PullRequest.Branches)
	assert.Equal(t, []Schedule{{Cron: "0 3 * * *"}}, tr.Schedules)
	assert.Equal(t, map[string]string{"GOFLAGS": "-mod=readonly"}, def.Variables)

	jobs := map[string]Job{}
	for _, j := range def.Jobs {
		jobs[j.Name] = j
	}
	assert.Equal(t, []string{}, jobs["lint"].Needs)
	assert.Equal(t, []string{"lint", "unit"}, jobs["integration"].Needs, "no needs: = every job of earlier stages")
	assert.Equal(t, []string{}, jobs["notify"].Needs, "needs: [] runs as soon as the run starts")
	assert.True(t, jobs["notify"].ExplicitNeeds)
	assert.Equal(t, []string{"lint", "unit", "integration"}, jobs["report"].Needs)
	assert.Equal(t, 1200, jobs["unit"].TimeoutSeconds)
	assert.Equal(t, []string{"linux", "docker"}, jobs["unit"].RunsOn)
	assert.Equal(t, "Test", jobs["unit"].Steps[0].Name)
	assert.Equal(t, "go test ./...\ngo vet ./...\n", jobs["unit"].Steps[0].Run)
	assert.Equal(t, "golangci-lint run", jobs["lint"].Steps[0].Name)
	assert.Equal(t, 30, jobs["report"].Artifacts.ExpireDays)
	assert.Equal(t, &Cache{Key: "go-${OPSHUB_REF}", Paths: []string{".cache/go"}}, jobs["unit"].Cache)
	assert.Equal(t, WhenOnFailure, jobs["report"].When)
	// Jobs are ordered by stage.
	assert.Equal(t, "check", def.Jobs[0].Stage)
	assert.Equal(t, "publish", def.Jobs[len(def.Jobs)-1].Stage)
}

func TestTriggerForms(t *testing.T) {
	base := "\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: alpine\n    steps: [true]\n"
	def, err := Parse([]byte("version: 1" + base))
	require.NoError(t, err)
	assert.True(t, def.Triggers.Push.Matches("any/branch"), "default: every push")
	assert.Nil(t, def.Triggers.PullRequest)

	def, err = Parse([]byte("version: 1\non: push" + base))
	require.NoError(t, err)
	assert.NotNil(t, def.Triggers.Push)

	def, err = Parse([]byte("version: 1\non:\n  push:\n    tags: ['v*']" + base))
	require.NoError(t, err)
	assert.Nil(t, def.Triggers.Push, "tags only")
	assert.True(t, def.Triggers.Tag.Matches("v1.2.0"))
	assert.False(t, def.Triggers.Tag.Matches("latest"))

	def, err = Parse([]byte("version: 1\non: [tag, manual]" + base))
	require.NoError(t, err)
	assert.True(t, def.Triggers.Tag.Matches("anything"))
	assert.Nil(t, def.Triggers.Push)

	def, err = Parse([]byte("version: 1\non:\n  push:\n  pull_request: {}" + base))
	require.NoError(t, err)
	assert.True(t, def.Triggers.Push.Matches("x"))
	assert.True(t, def.Triggers.PullRequest.Matches("main"))

	assert.Equal(t, []string{"on[0]:schedule_needs_cron"}, rules(problemsOf(t, "version: 1\non: [schedule]"+base)))
	assert.Equal(t, []string{"on[0]:oneof"}, rules(problemsOf(t, "version: 1\non: [deploy]"+base)))
	assert.Equal(t, []string{"on.schedule[0].cron:invalid_cron"}, rules(problemsOf(t, "version: 1\non:\n  schedule:\n    - cron: '61 * * * *'"+base)))
	assert.Equal(t, []string{"on.push.branchez:unknown_field"}, rules(problemsOf(t, "version: 1\non:\n  push:\n    branchez: [main]"+base)))
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"main", "main", true},
		{"main", "maint", false},
		{"release/*", "release/1.0", true},
		{"release/*", "release/1/hotfix", false},
		{"release/**", "release/1/hotfix", true},
		{"feature-*", "feature-login", true},
		{"v*", "v1.2.3", true},
		{"**", "any/thing", true},
		{"a.b", "axb", false}, // dots are literal
	}
	for _, c := range cases {
		assert.Equal(t, c.want, GlobMatch(c.pattern, c.name), "%s ~ %s", c.pattern, c.name)
	}
}

func TestValidationProblems(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
	}{
		{"syntax", "version: 1\njobs: [unclosed", []string{":syntax"}},
		{"empty", "", []string{":empty"}},
		{"missing top-level keys", "on: [push]\n", []string{"version:required", "stages:required", "jobs:required"}},
		{"unknown fields", "version: 1\nstages: [a]\ntrigger: x\njobs:\n  x:\n    stage: a\n    image: alpine\n    script: [ls]\n    steps: [ls]\n",
			[]string{"trigger:unknown_field", "jobs.x.script:unknown_field"}},
		{"version", "version: 2\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [ls]\n", []string{"version:unsupported_version"}},
		{"unknown stage and missing image", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: b\n    steps: [ls]\n",
			[]string{"jobs.x.image:required", "jobs.x.stage:unknown_stage"}},
		{"steps required", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n", []string{"jobs.x.steps:required"}},
		{"needs problems", `version: 1
stages: [a, b]
jobs:
  x:
    stage: a
    image: i
    needs: [x, ghost, y]
    steps: [ls]
  y:
    stage: b
    image: i
    steps: [ls]
`, []string{"jobs.x.needs[0]:self_need", "jobs.x.needs[1]:unknown_job", "jobs.x.needs[2]:need_later_stage"}},
		{"cycle", `version: 1
stages: [a]
jobs:
  x:
    stage: a
    image: i
    needs: [y]
    steps: [ls]
  y:
    stage: a
    image: i
    needs: [x]
    steps: [ls]
`, []string{"jobs.y.needs:cycle"}}, // reported where the loop closes
		{"bad values", `version: 1
stages: [a, a]
variables:
  1BAD: x
  OPSHUB_REF: x
jobs:
  Bad_Name:
    stage: a
    image: i
    steps: [ls]
  x:
    stage: a
    image: i
    when: sometimes
    timeout: 10h
    environment: Prod
    runs_on: ["UPPER"]
    steps: [ls]
    artifacts: ["../secrets"]
    deploy: { target: k8s, strategy: canary }
`, []string{"stages[1]:duplicate", "variables.1BAD:variable_name", "variables.OPSHUB_REF:reserved", "jobs.Bad_Name:job_name",
			"jobs.x.when:oneof", "jobs.x.timeout:range", "jobs.x.environment:pattern", "jobs.x.runs_on[0]:pattern",
			"jobs.x.steps:deploy_with_steps", "jobs.x.artifacts[0]:path", "jobs.x.deploy.strategy:oneof"}},
		{"deploy needs environment", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    deploy: { target: t }\n",
			[]string{"jobs.x.deploy:deploy_requires_environment"}},
		{"deploy jobs have no steps", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    environment: prod\n    image: alpine\n    steps: [make]\n    deploy: { target: t, version: \"app:1 2\" }\n",
			[]string{"jobs.x.deploy.version:pattern", "jobs.x.steps:deploy_with_steps"}},
		{"secrets", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [ls]\n    secrets: [lower, OK, OPSHUB_X, OK, 42]\n",
			[]string{"jobs.x.secrets[0]:pattern", "jobs.x.secrets[2]:reserved", "jobs.x.secrets[3]:duplicate", "jobs.x.secrets[4]:pattern"}},
		{"secrets is a list", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [ls]\n    secrets: {A: b}\n",
			[]string{"jobs.x.secrets:type"}},
		{"deploy jobs get no secrets", "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    environment: prod\n    secrets: [TOKEN]\n    deploy: { target: t }\n",
			[]string{"jobs.x.secrets:deploy_with_secrets"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.ElementsMatch(t, c.want, rules(problemsOf(t, c.src)))
		})
	}
}

func TestJobSecrets(t *testing.T) {
	def, err := Parse([]byte("version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [ls]\n    secrets: [NPM_TOKEN, _PRIVATE]\n  y:\n    stage: a\n    image: i\n    steps: [ls]\n    secrets: ONE\n  z:\n    stage: a\n    image: i\n    steps: [ls]\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"NPM_TOKEN", "_PRIVATE"}, def.Jobs[0].Secrets)
	assert.Equal(t, []string{"ONE"}, def.Jobs[1].Secrets, "a single name is a one-item list")
	assert.Empty(t, def.Jobs[2].Secrets)
}

func TestProblemPositions(t *testing.T) {
	ps := problemsOf(t, `version: 1
stages: [test]
jobs:
  unit:
    stage: tset
    image: golang:1.23
    steps: [go test ./...]
`)
	require.Len(t, ps, 1)
	assert.Equal(t, Problem{Line: 5, Column: 12, Path: "jobs.unit.stage", Rule: "unknown_stage", Param: "test"}, ps[0])
	assert.Contains(t, ps.Error(), "5:12 jobs.unit.stage: unknown_stage (test)")

	syntax := problemsOf(t, "version: 1\nstages: [a\njobs: {}\n")
	assert.Equal(t, "syntax", syntax[0].Rule)
	assert.Positive(t, syntax[0].Line)
}

func TestCyclePath(t *testing.T) {
	ps := problemsOf(t, "version: 1\nstages: [a]\njobs:\n  x: {stage: a, image: i, needs: [y], steps: [ls]}\n  y: {stage: a, image: i, needs: [z], steps: [ls]}\n  z: {stage: a, image: i, needs: [x], steps: [ls]}\n")
	require.Len(t, ps, 1)
	assert.Equal(t, "x → y → z → x", ps[0].Param)
}

func TestLimits(t *testing.T) {
	big := "version: 1\nstages: [a]\njobs:\n  x:\n    stage: a\n    image: i\n    steps: [" + strings.Repeat("ls, ", MaxSteps) + "ls]\n"
	assert.Contains(t, rules(problemsOf(t, big)), "jobs.x.steps:max")
	huge := strings.Repeat("#", MaxFileBytes+1)
	assert.Equal(t, []string{":file_too_large"}, rules(problemsOf(t, huge)))
}
