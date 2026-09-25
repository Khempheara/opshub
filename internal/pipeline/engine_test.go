package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

const (
	created  = store.JobStatusCreated
	queued   = store.JobStatusQueued
	running  = store.JobStatusRunning
	waiting  = store.JobStatusWaitingApproval
	passed   = store.JobStatusSucceeded
	failed   = store.JobStatusFailed
	canceled = store.JobStatusCanceled
	skipped  = store.JobStatusSkipped
)

func j(name string, status store.JobStatus, cond spec.When, needs ...string) node {
	if cond == "" {
		cond = spec.WhenOnSuccess
	}
	return node{Name: name, Status: status, Condition: cond, Needs: needs}
}

// simulate applies decide until nothing changes, treating Ready as "succeeds immediately"
// unless the job is listed in fail.
func simulate(jobs []node, fail ...string) map[string]string {
	for {
		ts := decide(jobs)
		if len(ts) == 0 {
			break
		}
		for _, t := range ts {
			for i := range jobs {
				if jobs[i].Name != t.Name {
					continue
				}
				switch {
				case !t.Ready:
					jobs[i].Status, jobs[i].Reason = skipped, t.Reason
				case contains(fail, t.Name):
					jobs[i].Status = failed
				default:
					jobs[i].Status = passed
				}
			}
		}
	}
	out := map[string]string{}
	for _, n := range jobs {
		out[n.Name] = string(n.Status) + reasonSuffix(n.Reason)
	}
	return out
}

func reasonSuffix(r string) string {
	if r == "" {
		return ""
	}
	return "/" + r
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestDecideHappyPathAndFailures(t *testing.T) {
	graph := func() []node {
		return []node{
			j("test", created, ""),
			j("lint", created, ""),
			j("build", created, "", "test", "lint"),
			j("deploy", created, spec.WhenManual, "build"),
			j("notify", created, spec.WhenAlways, "deploy"),
			j("report", created, spec.WhenOnFailure, "test", "lint"),
		}
	}
	assert.Equal(t, map[string]string{
		"test": "succeeded", "lint": "succeeded", "build": "succeeded", "deploy": "succeeded",
		"notify": "succeeded", "report": "skipped/not_needed",
	}, simulate(graph()))

	assert.Equal(t, map[string]string{
		"test": "failed", "lint": "succeeded", "build": "skipped/upstream_failed", "deploy": "skipped/upstream_failed",
		"notify": "succeeded", "report": "succeeded",
	}, simulate(graph(), "test"), "always runs anyway; on_failure runs; downstream is skipped")
}

func TestDecideWaitsForUnfinishedDependencies(t *testing.T) {
	jobs := []node{j("a", running, ""), j("b", created, "", "a"), j("c", created, "")}
	ts := decide(jobs)
	assert.Equal(t, []transition{{Name: "c", Ready: true}}, ts)

	// A skipped-but-not-needed dependency counts as success.
	jobs = []node{{Name: "report", Status: skipped, Reason: ReasonNotNeeded}, j("after", created, "", "report")}
	assert.Equal(t, []transition{{Name: "after", Ready: true}}, decide(jobs))
	// A canceled dependency does not.
	jobs = []node{j("a", canceled, ""), j("b", created, "", "a")}
	assert.Equal(t, []transition{{Name: "b", Reason: ReasonUpstreamFailed}}, decide(jobs))
}

func TestRunStatus(t *testing.T) {
	cases := []struct {
		jobs    []node
		started bool
		want    store.RunStatus
	}{
		{[]node{j("a", queued, ""), j("b", created, "", "a")}, false, store.RunStatusQueued},
		{[]node{j("a", queued, ""), j("b", passed, "")}, true, store.RunStatusRunning},
		{[]node{j("a", running, ""), j("b", waiting, "")}, true, store.RunStatusRunning},
		{[]node{j("a", passed, ""), j("b", waiting, "")}, true, store.RunStatusWaiting},
		{[]node{j("a", passed, ""), j("b", skipped, "")}, true, store.RunStatusSucceeded},
		{[]node{j("a", failed, ""), j("b", passed, "")}, true, store.RunStatusFailed},
		{[]node{j("a", canceled, ""), j("b", passed, "")}, true, store.RunStatusCanceled},
		{[]node{j("a", failed, ""), j("b", canceled, "")}, true, store.RunStatusFailed},
	}
	for i, c := range cases {
		assert.Equal(t, c.want, runStatus(c.jobs, c.started), "case %d", i)
	}
}

func TestDependents(t *testing.T) {
	jobs := []node{j("a", failed, ""), j("b", skipped, "", "a"), j("c", skipped, "", "b"), j("d", passed, ""), j("e", skipped, "", "d", "c")}
	assert.Equal(t, []string{"b", "c", "e"}, dependents(jobs, []string{"a"}))
	assert.Empty(t, dependents(jobs, []string{"e"}))
}
