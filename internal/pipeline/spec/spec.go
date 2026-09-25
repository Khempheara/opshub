// Package spec parses and validates `.opshub.yml`, the pipeline definition (docs/pipelines.md).
// Parsing walks YAML nodes so every problem carries a line and column; problems carry a
// stable rule code (and parameter) instead of an English sentence so the UI can translate
// them. The result is a normalized Definition that is snapshotted on each run.
package spec

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Limits.
const (
	MaxFileBytes    = 256 << 10
	MaxJobs         = 100
	MaxSteps        = 50
	MaxStages       = 20
	MaxVariables    = 100
	MaxVariableLen  = 4096
	MaxSchedules    = 10
	DefaultTimeout  = 60 * time.Minute
	MinTimeout      = time.Minute
	MaxTimeout      = 6 * time.Hour
	MaxArtifactDays = 90
)

// When controls when a job runs relative to its dependencies.
type When string

const (
	WhenOnSuccess When = "on_success" // all dependencies succeeded (default)
	WhenOnFailure When = "on_failure" // at least one dependency failed
	WhenAlways    When = "always"     // dependencies finished, whatever their result
	WhenManual    When = "manual"     // like on_success, then waits for an approval
)

// Definition is a validated pipeline.
type Definition struct {
	Version   int               `json:"version"`
	Triggers  Triggers          `json:"triggers"`
	Stages    []string          `json:"stages"`
	Variables map[string]string `json:"variables"`
	Jobs      []Job             `json:"jobs"` // ordered by stage, then as written
}

// Triggers says which events start a run. Manual runs are always possible.
type Triggers struct {
	Push        *BranchFilter `json:"push,omitempty"`         // branch pushes
	Tag         *BranchFilter `json:"tag,omitempty"`          // tag pushes (patterns in Branches)
	PullRequest *BranchFilter `json:"pull_request,omitempty"` // filtered by target branch
	Schedules   []Schedule    `json:"schedules,omitempty"`
}

// BranchFilter matches refs with glob patterns (`*` within a segment, `**` across). Empty =
// every ref.
type BranchFilter struct {
	Branches []string `json:"branches,omitempty"`
}

// Matches reports whether a branch or tag name matches the filter.
func (f *BranchFilter) Matches(name string) bool {
	if f == nil {
		return false
	}
	if len(f.Branches) == 0 {
		return true
	}
	for _, p := range f.Branches {
		if GlobMatch(p, name) {
			return true
		}
	}
	return false
}

// Schedule is a cron trigger; scheduled runs use the project's default branch.
type Schedule struct {
	Cron string `json:"cron"`
}

// Job is one unit of work, run on a single runner.
type Job struct {
	Name           string            `json:"name"`
	Stage          string            `json:"stage"`
	StageIndex     int               `json:"stage_index"`
	Needs          []string          `json:"needs"`          // resolved: explicit needs, or every job of earlier stages
	ExplicitNeeds  bool              `json:"explicit_needs"` // `needs:` was written
	Image          string            `json:"image,omitempty"`
	Steps          []Step            `json:"steps"`
	Variables      map[string]string `json:"variables"`
	When           When              `json:"when"`
	Environment    string            `json:"environment,omitempty"`
	RunsOn         []string          `json:"runs_on"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Artifacts      *Artifacts        `json:"artifacts,omitempty"`
	Cache          *Cache            `json:"cache,omitempty"`
	Deploy         *Deploy           `json:"deploy,omitempty"`
}

// Step is one shell command.
type Step struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// Artifacts are files kept after the job (uploaded by the runner, Module 5).
type Artifacts struct {
	Paths      []string `json:"paths"`
	ExpireDays int      `json:"expire_days"`
}

// Cache is restored before and saved after the job (Module 5).
type Cache struct {
	Key   string   `json:"key"`
	Paths []string `json:"paths"`
}

// Deploy describes a deployment performed by OpsHub for the job (Module 6). Version is the
// container image to deploy; ${VAR} references are expanded with the job's variables when
// the job starts.
type Deploy struct {
	Target   string `json:"target"`
	Strategy string `json:"strategy"`
	Version  string `json:"version"`
}

// DefaultDeployVersion is used when a deploy block has no version.
const DefaultDeployVersion = "${DEPLOY_VERSION}"

// Problem is one validation error. Rule is a stable code (see docs/pipelines.md); Param
// holds a rule parameter such as an allowed set or a limit.
type Problem struct {
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Path   string `json:"path"`
	Rule   string `json:"rule"`
	Param  string `json:"param,omitempty"`
}

func (p Problem) String() string {
	s := fmt.Sprintf("%d:%d %s: %s", p.Line, p.Column, p.Path, p.Rule)
	if p.Param != "" {
		s += " (" + p.Param + ")"
	}
	return s
}

// Problems is returned when a definition is invalid.
type Problems []Problem

func (ps Problems) Error() string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return "invalid pipeline: " + strings.Join(parts, "; ")
}

var (
	jobNamePattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$`)
	stageNamePattern = jobNamePattern
	envNamePattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$`)
	varNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	labelPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	refGlobPattern   = regexp.MustCompile(`^[A-Za-z0-9._/*-]{1,255}$`)
	targetPattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$`)
)

// parser collects problems while walking the document.
type parser struct {
	problems Problems
}

func (p *parser) add(n *yaml.Node, path, rule, param string) {
	line, col := 0, 0
	if n != nil {
		line, col = n.Line, n.Column
	}
	p.problems = append(p.problems, Problem{Line: line, Column: col, Path: path, Rule: rule, Param: param})
}

// Parse validates a `.opshub.yml` document.
func Parse(src []byte) (*Definition, error) {
	if len(src) > MaxFileBytes {
		return nil, Problems{{Line: 1, Column: 1, Path: "", Rule: "file_too_large", Param: strconv.Itoa(MaxFileBytes)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, Problems{syntaxProblem(err)}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, Problems{{Line: 1, Column: 1, Rule: "empty"}}
	}
	root := doc.Content[0]
	p := &parser{}
	def := p.definition(root)
	if len(p.problems) > 0 {
		slices.SortStableFunc(p.problems, func(a, b Problem) int {
			if a.Line != b.Line {
				return a.Line - b.Line
			}
			return a.Column - b.Column
		})
		return nil, p.problems
	}
	return def, nil
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

func syntaxProblem(err error) Problem {
	line := 1
	if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: ")
	if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "line ") {
		msg = msg[i+2:]
	}
	return Problem{Line: line, Column: 1, Rule: "syntax", Param: msg}
}

// mapping returns key → (key node, value node) and reports unknown keys.
func (p *parser) mapping(n *yaml.Node, path string, allowed ...string) map[string][2]*yaml.Node {
	out := map[string][2]*yaml.Node{}
	if n.Kind != yaml.MappingNode {
		p.add(n, path, "type", "mapping")
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		child := join(path, k.Value)
		if len(allowed) > 0 && !slices.Contains(allowed, k.Value) {
			p.add(k, child, "unknown_field", strings.Join(allowed, " "))
			continue
		}
		if _, dup := out[k.Value]; dup {
			p.add(k, child, "duplicate", "")
			continue
		}
		out[k.Value] = [2]*yaml.Node{k, v}
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (p *parser) scalar(n *yaml.Node, path string) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		p.add(n, path, "type", "string")
		return "", false
	}
	return n.Value, true
}

func (p *parser) stringList(n *yaml.Node, path string, pattern *regexp.Regexp, maxItems int) []string {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!null" { // a single value is a one-item list
		n = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}, Line: n.Line, Column: n.Column}
	}
	if n.Kind != yaml.SequenceNode {
		p.add(n, path, "type", "list")
		return nil
	}
	if maxItems > 0 && len(n.Content) > maxItems {
		p.add(n, path, "max", strconv.Itoa(maxItems))
	}
	out := []string{}
	for i, item := range n.Content {
		ip := fmt.Sprintf("%s[%d]", path, i)
		s, ok := p.scalar(item, ip)
		if !ok {
			continue
		}
		if pattern != nil && !pattern.MatchString(s) {
			p.add(item, ip, "pattern", "")
			continue
		}
		out = append(out, s)
	}
	return out
}

func (p *parser) variables(n *yaml.Node, path string) map[string]string {
	out := map[string]string{}
	m := p.mapping(n, path)
	if len(m) > MaxVariables {
		p.add(n, path, "max", strconv.Itoa(MaxVariables))
	}
	for k, kv := range m {
		vp := join(path, k)
		if !varNamePattern.MatchString(k) {
			p.add(kv[0], vp, "variable_name", "")
			continue
		}
		if strings.HasPrefix(strings.ToUpper(k), "OPSHUB_") {
			p.add(kv[0], vp, "reserved", "OPSHUB_")
			continue
		}
		s, ok := p.scalar(kv[1], vp)
		if !ok {
			continue
		}
		if len(s) > MaxVariableLen {
			p.add(kv[1], vp, "max", strconv.Itoa(MaxVariableLen))
			continue
		}
		out[k] = s
	}
	return out
}

func (p *parser) definition(root *yaml.Node) *Definition {
	top := p.mapping(root, "", "version", "on", "stages", "variables", "jobs")
	def := &Definition{Variables: map[string]string{}}

	if v, ok := top["version"]; !ok {
		p.add(root, "version", "required", "")
	} else if s, ok := p.scalar(v[1], "version"); ok {
		if s != "1" {
			p.add(v[1], "version", "unsupported_version", "1")
		}
		def.Version = 1
	}

	if v, ok := top["on"]; ok {
		def.Triggers = p.triggers(v[1])
	} else {
		def.Triggers = Triggers{Push: &BranchFilter{}}
	}

	if v, ok := top["stages"]; !ok {
		p.add(root, "stages", "required", "")
	} else {
		def.Stages = p.stringList(v[1], "stages", stageNamePattern, MaxStages)
		if len(def.Stages) == 0 && v[1].Kind == yaml.SequenceNode {
			p.add(v[1], "stages", "empty", "")
		}
		seen := map[string]bool{}
		for i, s := range def.Stages {
			if seen[s] {
				p.add(v[1].Content[i], fmt.Sprintf("stages[%d]", i), "duplicate", "")
			}
			seen[s] = true
		}
	}

	if v, ok := top["variables"]; ok {
		def.Variables = p.variables(v[1], "variables")
	}

	jobsNode, ok := top["jobs"]
	if !ok {
		p.add(root, "jobs", "required", "")
		return def
	}
	jm := jobsNode[1]
	if jm.Kind != yaml.MappingNode {
		p.add(jm, "jobs", "type", "mapping")
		return def
	}
	if len(jm.Content)/2 > MaxJobs {
		p.add(jm, "jobs", "max", strconv.Itoa(MaxJobs))
	}
	if len(jm.Content) == 0 {
		p.add(jm, "jobs", "empty", "")
	}
	needNodes := map[string]*yaml.Node{}
	names := map[string]bool{}
	for i := 0; i+1 < len(jm.Content); i += 2 {
		k, v := jm.Content[i], jm.Content[i+1]
		jp := join("jobs", k.Value)
		if !jobNamePattern.MatchString(k.Value) {
			p.add(k, jp, "job_name", "")
			continue
		}
		if names[k.Value] {
			p.add(k, jp, "duplicate", "")
			continue
		}
		names[k.Value] = true
		job, needsNode := p.job(k.Value, v, jp, def.Stages)
		if job != nil {
			def.Jobs = append(def.Jobs, *job)
			needNodes[job.Name] = needsNode
		}
	}
	p.resolveNeeds(def, needNodes)
	slices.SortStableFunc(def.Jobs, func(a, b Job) int { return a.StageIndex - b.StageIndex })
	return def
}

func (p *parser) triggers(n *yaml.Node) Triggers {
	var t Triggers
	if n.Kind == yaml.ScalarNode || n.Kind == yaml.SequenceNode {
		for i, ev := range p.stringList(n, "on", nil, 0) {
			ip := fmt.Sprintf("on[%d]", i)
			switch ev {
			case "push":
				t.Push = &BranchFilter{}
			case "pull_request":
				t.PullRequest = &BranchFilter{}
			case "tag":
				t.Tag = &BranchFilter{}
			case "manual":
			case "schedule":
				p.add(n, ip, "schedule_needs_cron", "")
			default:
				p.add(n, ip, "oneof", "push pull_request tag manual")
			}
		}
		return t
	}
	m := p.mapping(n, "on", "push", "pull_request", "tag", "schedule", "manual")
	// fields parses `key: {field: [patterns]}`; a null or empty value means "everything".
	fields := func(key string, allowed ...string) (present bool, got map[string][]string) {
		kv, ok := m[key]
		if !ok {
			return false, nil
		}
		got = map[string][]string{}
		if kv[1].Tag == "!!null" {
			return true, got
		}
		kp := join("on", key)
		for f, fv := range p.mapping(kv[1], kp, allowed...) {
			got[f] = p.stringList(fv[1], join(kp, f), refGlobPattern, 50)
		}
		return true, got
	}
	if ok, f := fields("push", "branches", "tags"); ok {
		branches, hasBranches := f["branches"]
		tags, hasTags := f["tags"]
		if hasBranches || !hasTags { // `push: {tags: [...]}` alone means tags only
			t.Push = &BranchFilter{Branches: branches}
		}
		if hasTags {
			t.Tag = &BranchFilter{Branches: tags}
		}
	}
	if ok, f := fields("pull_request", "branches"); ok {
		t.PullRequest = &BranchFilter{Branches: f["branches"]}
	}
	if ok, f := fields("tag", "patterns"); ok && t.Tag == nil {
		t.Tag = &BranchFilter{Branches: f["patterns"]}
	}
	if kv, ok := m["schedule"]; ok {
		if kv[1].Kind != yaml.SequenceNode {
			p.add(kv[1], "on.schedule", "type", "list")
		} else {
			if len(kv[1].Content) > MaxSchedules {
				p.add(kv[1], "on.schedule", "max", strconv.Itoa(MaxSchedules))
			}
			for i, item := range kv[1].Content {
				ip := fmt.Sprintf("on.schedule[%d]", i)
				sm := p.mapping(item, ip, "cron")
				c, ok := sm["cron"]
				if !ok {
					if item.Kind == yaml.MappingNode {
						p.add(item, join(ip, "cron"), "required", "")
					}
					continue
				}
				expr, ok := p.scalar(c[1], join(ip, "cron"))
				if !ok {
					continue
				}
				if _, err := ParseCron(expr); err != nil {
					p.add(c[1], join(ip, "cron"), "invalid_cron", err.Error())
					continue
				}
				t.Schedules = append(t.Schedules, Schedule{Cron: expr})
			}
		}
	}
	if kv, ok := m["manual"]; ok {
		if kv[1].Kind != yaml.ScalarNode || (kv[1].Value != "true" && kv[1].Value != "false") {
			p.add(kv[1], "on.manual", "type", "boolean")
		}
	}
	return t
}

func (p *parser) job(name string, n *yaml.Node, jp string, stages []string) (*Job, *yaml.Node) {
	m := p.mapping(n, jp, "stage", "image", "needs", "steps", "variables", "when", "environment",
		"runs_on", "timeout", "artifacts", "cache", "deploy")
	if n.Kind != yaml.MappingNode {
		return nil, nil
	}
	job := &Job{
		Name: name, When: WhenOnSuccess, Variables: map[string]string{}, Steps: []Step{}, RunsOn: []string{},
		TimeoutSeconds: int(DefaultTimeout.Seconds()),
	}

	if v, ok := m["stage"]; !ok {
		p.add(n, join(jp, "stage"), "required", "")
	} else if s, ok := p.scalar(v[1], join(jp, "stage")); ok {
		idx := slices.Index(stages, s)
		if idx < 0 {
			p.add(v[1], join(jp, "stage"), "unknown_stage", strings.Join(stages, " "))
		}
		job.Stage, job.StageIndex = s, idx
	}

	if v, ok := m["image"]; ok {
		if s, ok := p.scalar(v[1], join(jp, "image")); ok {
			if len(s) > 255 || strings.ContainsAny(s, " \t\n") || s == "" {
				p.add(v[1], join(jp, "image"), "pattern", "")
			}
			job.Image = s
		}
	}

	if v, ok := m["steps"]; ok {
		job.Steps = p.steps(v[1], join(jp, "steps"))
	}

	if v, ok := m["variables"]; ok {
		job.Variables = p.variables(v[1], join(jp, "variables"))
	}

	if v, ok := m["when"]; ok {
		if s, ok := p.scalar(v[1], join(jp, "when")); ok {
			switch When(s) {
			case WhenOnSuccess, WhenOnFailure, WhenAlways, WhenManual:
				job.When = When(s)
			default:
				p.add(v[1], join(jp, "when"), "oneof", "on_success on_failure always manual")
			}
		}
	}

	if v, ok := m["environment"]; ok {
		if s, ok := p.scalar(v[1], join(jp, "environment")); ok {
			if !envNamePattern.MatchString(s) {
				p.add(v[1], join(jp, "environment"), "pattern", "")
			}
			job.Environment = s
		}
	}

	if v, ok := m["runs_on"]; ok {
		job.RunsOn = p.stringList(v[1], join(jp, "runs_on"), labelPattern, 10)
	}

	if v, ok := m["timeout"]; ok {
		if s, ok := p.scalar(v[1], join(jp, "timeout")); ok {
			switch d, err := time.ParseDuration(s); {
			case err != nil:
				p.add(v[1], join(jp, "timeout"), "duration", "")
			case d < MinTimeout || d > MaxTimeout:
				p.add(v[1], join(jp, "timeout"), "range", "1m-6h")
			default:
				job.TimeoutSeconds = int(d.Seconds())
			}
		}
	}

	if v, ok := m["artifacts"]; ok {
		job.Artifacts = p.artifacts(v[1], join(jp, "artifacts"))
	}
	if v, ok := m["cache"]; ok {
		job.Cache = p.cache(v[1], join(jp, "cache"))
	}
	if v, ok := m["deploy"]; ok {
		job.Deploy = p.deploy(v[1], join(jp, "deploy"))
		if job.Environment == "" {
			p.add(v[0], join(jp, "deploy"), "deploy_requires_environment", "")
		}
		if s, ok := m["steps"]; ok {
			p.add(s[0], join(jp, "steps"), "deploy_with_steps", "")
		}
	}

	if len(job.Steps) == 0 && job.Deploy == nil {
		if _, hasSteps := m["steps"]; !hasSteps {
			p.add(n, join(jp, "steps"), "required", "")
		}
	}
	if len(job.Steps) > 0 && job.Image == "" {
		p.add(n, join(jp, "image"), "required", "")
	}

	var needsNode *yaml.Node
	if v, ok := m["needs"]; ok {
		needsNode = v[1]
		job.ExplicitNeeds = true
		job.Needs = p.stringList(v[1], join(jp, "needs"), nil, MaxJobs)
	}
	return job, needsNode
}

func (p *parser) steps(n *yaml.Node, path string) []Step {
	if n.Kind != yaml.SequenceNode {
		p.add(n, path, "type", "list")
		return nil
	}
	if len(n.Content) == 0 {
		p.add(n, path, "empty", "")
	}
	if len(n.Content) > MaxSteps {
		p.add(n, path, "max", strconv.Itoa(MaxSteps))
	}
	out := []Step{}
	for i, item := range n.Content {
		ip := fmt.Sprintf("%s[%d]", path, i)
		if item.Kind == yaml.ScalarNode {
			if strings.TrimSpace(item.Value) == "" {
				p.add(item, ip, "empty", "")
				continue
			}
			out = append(out, Step{Name: firstLine(item.Value), Run: item.Value})
			continue
		}
		sm := p.mapping(item, ip, "name", "run")
		r, ok := sm["run"]
		if !ok {
			if item.Kind == yaml.MappingNode {
				p.add(item, join(ip, "run"), "required", "")
			}
			continue
		}
		run, ok := p.scalar(r[1], join(ip, "run"))
		if !ok {
			continue
		}
		if strings.TrimSpace(run) == "" {
			p.add(r[1], join(ip, "run"), "empty", "")
			continue
		}
		step := Step{Name: firstLine(run), Run: run}
		if nm, ok := sm["name"]; ok {
			if s, ok := p.scalar(nm[1], join(ip, "name")); ok && s != "" {
				if len(s) > 200 {
					p.add(nm[1], join(ip, "name"), "max", "200")
				}
				step.Name = s
			}
		}
		out = append(out, step)
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (p *parser) paths(n *yaml.Node, path string) []string {
	out := p.stringList(n, path, nil, 50)
	for i, s := range out {
		if s == "" || path2Unsafe(s) {
			p.add(n, fmt.Sprintf("%s[%d]", path, i), "path", "")
		}
	}
	return out
}

// path2Unsafe rejects absolute paths and parent-directory escapes: artifacts and caches are
// relative to the job's workspace.
func path2Unsafe(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return true
	}
	clean := path.Clean(s)
	return clean == ".." || strings.HasPrefix(clean, "../")
}

func (p *parser) artifacts(n *yaml.Node, path string) *Artifacts {
	a := &Artifacts{ExpireDays: 7}
	if n.Kind == yaml.SequenceNode || n.Kind == yaml.ScalarNode {
		a.Paths = p.paths(n, path)
		return a
	}
	m := p.mapping(n, path, "paths", "expire_in")
	if v, ok := m["paths"]; ok {
		a.Paths = p.paths(v[1], join(path, "paths"))
	} else if n.Kind == yaml.MappingNode {
		p.add(n, join(path, "paths"), "required", "")
	}
	if v, ok := m["expire_in"]; ok {
		if s, ok := p.scalar(v[1], join(path, "expire_in")); ok {
			days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
			if err != nil || !strings.HasSuffix(s, "d") || days < 1 || days > MaxArtifactDays {
				p.add(v[1], join(path, "expire_in"), "range", fmt.Sprintf("1d-%dd", MaxArtifactDays))
			} else {
				a.ExpireDays = days
			}
		}
	}
	return a
}

func (p *parser) cache(n *yaml.Node, path string) *Cache {
	m := p.mapping(n, path, "key", "paths")
	c := &Cache{}
	if v, ok := m["key"]; ok {
		if s, ok := p.scalar(v[1], join(path, "key")); ok {
			if s == "" || len(s) > 200 {
				p.add(v[1], join(path, "key"), "pattern", "")
			}
			c.Key = s
		}
	} else if n.Kind == yaml.MappingNode {
		p.add(n, join(path, "key"), "required", "")
	}
	if v, ok := m["paths"]; ok {
		c.Paths = p.paths(v[1], join(path, "paths"))
	} else if n.Kind == yaml.MappingNode {
		p.add(n, join(path, "paths"), "required", "")
	}
	return c
}

func (p *parser) deploy(n *yaml.Node, path string) *Deploy {
	m := p.mapping(n, path, "target", "strategy", "version")
	d := &Deploy{Strategy: "rolling", Version: DefaultDeployVersion}
	if v, ok := m["version"]; ok {
		if s, ok := p.scalar(v[1], join(path, "version")); ok {
			if s == "" || len(s) > 255 || strings.ContainsAny(s, " \t\n") {
				p.add(v[1], join(path, "version"), "pattern", "")
			}
			d.Version = s
		}
	}
	if v, ok := m["target"]; ok {
		if s, ok := p.scalar(v[1], join(path, "target")); ok {
			if !targetPattern.MatchString(s) {
				p.add(v[1], join(path, "target"), "pattern", "")
			}
			d.Target = s
		}
	} else if n.Kind == yaml.MappingNode {
		p.add(n, join(path, "target"), "required", "")
	}
	if v, ok := m["strategy"]; ok {
		if s, ok := p.scalar(v[1], join(path, "strategy")); ok {
			if s != "rolling" && s != "blue_green" {
				p.add(v[1], join(path, "strategy"), "oneof", "rolling blue_green")
			}
			d.Strategy = s
		}
	}
	return d
}

// resolveNeeds checks explicit needs and fills implicit ones (every job of earlier stages),
// then rejects cycles.
func (p *parser) resolveNeeds(def *Definition, needNodes map[string]*yaml.Node) {
	byName := map[string]*Job{}
	for i := range def.Jobs {
		byName[def.Jobs[i].Name] = &def.Jobs[i]
	}
	for i := range def.Jobs {
		j := &def.Jobs[i]
		if !j.ExplicitNeeds {
			j.Needs = []string{}
			for _, other := range def.Jobs {
				if other.StageIndex >= 0 && j.StageIndex >= 0 && other.StageIndex < j.StageIndex {
					j.Needs = append(j.Needs, other.Name)
				}
			}
			continue
		}
		node := needNodes[j.Name]
		seen := map[string]bool{}
		for k, need := range j.Needs {
			np := fmt.Sprintf("jobs.%s.needs[%d]", j.Name, k)
			item := node
			if node != nil && node.Kind == yaml.SequenceNode && k < len(node.Content) {
				item = node.Content[k]
			}
			switch other, ok := byName[need]; {
			case need == j.Name:
				p.add(item, np, "self_need", "")
			case !ok:
				p.add(item, np, "unknown_job", need)
			case seen[need]:
				p.add(item, np, "duplicate", "")
			case other.StageIndex > j.StageIndex && j.StageIndex >= 0:
				p.add(item, np, "need_later_stage", other.Stage)
			}
			seen[need] = true
		}
	}
	if len(p.problems) > 0 {
		return
	}
	// Cycles are only possible among same-stage jobs; report the first one found.
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var stack []string
	var visit func(name string) bool
	visit = func(name string) bool {
		state[name] = visiting
		stack = append(stack, name)
		for _, n := range byName[name].Needs {
			switch state[n] {
			case visiting:
				start := slices.Index(stack, n)
				cycle := append(slices.Clone(stack[start:]), n)
				p.add(needNodes[name], "jobs."+name+".needs", "cycle", strings.Join(cycle, " → "))
				return false
			case unvisited:
				if !visit(n) {
					return false
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return true
	}
	for _, j := range def.Jobs {
		if state[j.Name] == unvisited && !visit(j.Name) {
			return
		}
	}
}

// GlobMatch matches a ref name against a pattern: `*` matches within a path segment, `**`
// matches across segments. `release/*` matches `release/1.0` but not `release/1/x`.
func GlobMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, name)
		return err == nil && ok
	}
	// Translate to a regexp: ** → .*, * → [^/]*, other characters literal.
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}
