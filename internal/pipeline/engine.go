package pipeline

import (
	"slices"

	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

// Failure and skip reasons recorded on jobs (translated by the UI).
const (
	ReasonUpstreamFailed   = "upstream_failed"    // skipped: a dependency failed or was canceled
	ReasonNotNeeded        = "not_needed"         // skipped: its `when` condition wasn't met
	ReasonRejected         = "rejected"           // failed: an approver rejected it
	ReasonBranchNotAllowed = "branch_not_allowed" // failed: protected environment, branch not allowed
	ReasonEnvNotFound      = "environment_not_found"
	ReasonTimeout          = "timeout"
	ReasonNoRunner         = "no_runner"   // failed: queued for 24 h without a runner
	ReasonStepFailed       = "step_failed" // failed: a step exited non-zero
	ReasonRunnerError      = "runner_error"
)

// node is the engine's view of a job's current attempt.
type node struct {
	Name      string
	Status    store.JobStatus
	Reason    string
	Needs     []string
	Condition spec.When
}

// terminal reports whether a job status is final.
func terminal(s store.JobStatus) bool {
	switch s {
	case store.JobStatusSucceeded, store.JobStatusFailed, store.JobStatusCanceled, store.JobStatusSkipped:
		return true
	}
	return false
}

// ok: the dependency counts as successful (skipped only because it wasn't needed).
func ok(n node) bool {
	return n.Status == store.JobStatusSucceeded || (n.Status == store.JobStatusSkipped && n.Reason == ReasonNotNeeded)
}

// transition is a decided change for a job in "created" state.
type transition struct {
	Name   string
	Ready  bool   // dependencies satisfied: next, gate for approval or queue
	Reason string // when not Ready: skip reason
}

// decide returns the transitions for created jobs whose dependencies have all finished.
// Callers apply them and call decide again until it returns nothing (skips cascade).
func decide(jobs []node) []transition {
	byName := make(map[string]node, len(jobs))
	for _, j := range jobs {
		byName[j.Name] = j
	}
	var out []transition
	for _, j := range jobs {
		if j.Status != store.JobStatusCreated {
			continue
		}
		deps := make([]node, 0, len(j.Needs))
		finished := true
		for _, n := range j.Needs {
			d, found := byName[n]
			if !found {
				continue // defensive: validated definitions have no dangling needs
			}
			if !terminal(d.Status) {
				finished = false
				break
			}
			deps = append(deps, d)
		}
		if !finished {
			continue
		}
		allOK := !slices.ContainsFunc(deps, func(d node) bool { return !ok(d) })
		switch j.Condition {
		case spec.WhenAlways:
			out = append(out, transition{Name: j.Name, Ready: true})
		case spec.WhenOnFailure:
			if !allOK {
				out = append(out, transition{Name: j.Name, Ready: true})
			} else {
				out = append(out, transition{Name: j.Name, Reason: ReasonNotNeeded})
			}
		default: // on_success, manual
			if allOK {
				out = append(out, transition{Name: j.Name, Ready: true})
			} else {
				out = append(out, transition{Name: j.Name, Reason: ReasonUpstreamFailed})
			}
		}
	}
	return out
}

// runStatus derives a run's status from its jobs' current attempts.
func runStatus(jobs []node, started bool) store.RunStatus {
	all := true
	var failed, canceled, running, queued, waiting bool
	for _, j := range jobs {
		switch j.Status {
		case store.JobStatusFailed:
			failed = true
		case store.JobStatusCanceled:
			canceled = true
		case store.JobStatusRunning:
			running = true
		case store.JobStatusQueued:
			queued = true
		case store.JobStatusWaitingApproval:
			waiting = true
		}
		if !terminal(j.Status) {
			all = false
		}
	}
	switch {
	case all && failed:
		return store.RunStatusFailed
	case all && canceled:
		return store.RunStatusCanceled
	case all:
		return store.RunStatusSucceeded
	case running:
		return store.RunStatusRunning
	case queued && started:
		return store.RunStatusRunning
	case queued:
		return store.RunStatusQueued
	case waiting:
		return store.RunStatusWaiting
	}
	return store.RunStatusRunning // created jobs waiting on dependencies
}

// dependents returns the names of every job that (transitively) needs one of names.
func dependents(jobs []node, names []string) []string {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	for changed := true; changed; {
		changed = false
		for _, j := range jobs {
			if set[j.Name] {
				continue
			}
			for _, n := range j.Needs {
				if set[n] {
					set[j.Name], changed = true, true
					break
				}
			}
		}
	}
	var out []string
	for _, j := range jobs {
		if set[j.Name] && !slices.Contains(names, j.Name) {
			out = append(out, j.Name)
		}
	}
	return out
}
