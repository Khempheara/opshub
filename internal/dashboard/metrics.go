package dashboard

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/store"
)

// PipelineSummary covers runs created in the range. SuccessRate is succeeded ÷ (succeeded +
// failed); canceled and unfinished runs don't count. Durations are of finished runs.
type PipelineSummary struct {
	Runs        int32    `json:"runs"`
	Succeeded   int32    `json:"succeeded"`
	Failed      int32    `json:"failed"`
	Canceled    int32    `json:"canceled"`
	SuccessRate *float64 `json:"success_rate"`
	DurationP50 *float64 `json:"duration_p50_s"`
	DurationP95 *float64 `json:"duration_p95_s"`
}

// PipelinePoint is one day or week.
type PipelinePoint struct {
	Period      string   `json:"period"`
	Runs        int32    `json:"runs"`
	Succeeded   int32    `json:"succeeded"`
	Failed      int32    `json:"failed"`
	SuccessRate *float64 `json:"success_rate"`
	DurationP50 *float64 `json:"duration_p50_s"`
	DurationP95 *float64 `json:"duration_p95_s"`
}

// ProjectPipelines is one project's runs in the range.
type ProjectPipelines struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Runs        int32     `json:"runs"`
	SuccessRate *float64  `json:"success_rate"`
	DurationP50 *float64  `json:"duration_p50_s"`
	LastStatus  string    `json:"last_status"`
	LastRunAt   time.Time `json:"last_run_at"`
}

// PipelineStats is GET /orgs/{id}/dashboard/pipelines.
type PipelineStats struct {
	Range    Range              `json:"range"`
	Summary  PipelineSummary    `json:"summary"`
	Trend    []PipelinePoint    `json:"trend"`
	Projects []ProjectPipelines `json:"projects"`
}

// Pipelines returns pipeline statistics for the projects the caller can see.
func (s *Service) Pipelines(ctx context.Context, orgID uuid.UUID, in Query) (PipelineStats, error) {
	q := store.New(s.pool)
	sc, err := s.resolve(ctx, q, orgID, in)
	if err != nil {
		return PipelineStats{}, err
	}
	sum, err := q.PipelineSummary(ctx, store.PipelineSummaryParams{
		OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To, SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return PipelineStats{}, err
	}
	trend, err := q.PipelineTrend(ctx, store.PipelineTrendParams{
		Bucket: sc.rng.Bucket, Tz: sc.rng.TZ, OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To,
		SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return PipelineStats{}, err
	}
	byProject, err := q.PipelineByProject(ctx, store.PipelineByProjectParams{
		OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To, SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return PipelineStats{}, err
	}
	out := PipelineStats{
		Range: sc.rng,
		Summary: PipelineSummary{
			Runs: sum.Runs, Succeeded: sum.Succeeded, Failed: sum.Failed, Canceled: sum.Canceled,
			SuccessRate: ratio(sum.Succeeded, sum.Succeeded+sum.Failed), DurationP50: seconds(sum.DurationP50), DurationP95: seconds(sum.DurationP95),
		},
		Trend:    []PipelinePoint{},
		Projects: make([]ProjectPipelines, 0, len(byProject)),
	}
	got := map[string]store.PipelineTrendRow{}
	for _, t := range trend {
		got[t.Period] = t
	}
	for _, p := range periods(sc.rng, sc.loc) {
		t := got[p]
		out.Trend = append(out.Trend, PipelinePoint{
			Period: p, Runs: t.Runs, Succeeded: t.Succeeded, Failed: t.Failed, SuccessRate: ratio(t.Succeeded, t.Succeeded+t.Failed),
			DurationP50: seconds(orNone(t.Runs, t.DurationP50)), DurationP95: seconds(orNone(t.Runs, t.DurationP95)),
		})
	}
	for _, p := range byProject {
		out.Projects = append(out.Projects, ProjectPipelines{
			ID: p.ID, Name: p.Name, Slug: p.Slug, Runs: p.Runs, SuccessRate: ratio(p.Succeeded, p.Succeeded+p.Failed),
			DurationP50: seconds(p.DurationP50), LastStatus: p.LastStatus, LastRunAt: p.LastRunAt,
		})
	}
	return out, nil
}

// orNone is v, or -1 for an empty period (its zero value isn't a measurement).
func orNone(n int32, v float64) float64 {
	if n == 0 {
		return -1
	}
	return v
}

// Deployments counts successful changes (deployments to production that aren't rollbacks).
type Deployments struct {
	Succeeded int32   `json:"succeeded"`
	PerDay    float64 `json:"per_day"`
}

// ChangeFailureRate is failed changes (failed, or rolled back later) ÷ changes.
type ChangeFailureRate struct {
	Failed  int32    `json:"failed"`
	Changes int32    `json:"changes"`
	Rate    *float64 `json:"rate"`
}

// LeadTime runs from commit to successful production deployment.
type LeadTime struct {
	Median  *float64 `json:"median_s"`
	P95     *float64 `json:"p95_s"`
	Samples int32    `json:"samples"`
}

// TimeToRestore runs from a failed change until production works again; Open counts failed
// changes not restored yet.
type TimeToRestore struct {
	Median   *float64 `json:"median_s"`
	Restored int32    `json:"restored"`
	Open     int32    `json:"open"`
}

// AlertRecovery is organization-wide: alerts resolved in the range and the median time from
// firing to resolved.
type AlertRecovery struct {
	Resolved int32    `json:"resolved"`
	Median   *float64 `json:"median_s"`
}

// DeploymentPoint is one day or week: changes that succeeded and stayed, and changes that
// failed or were rolled back later.
type DeploymentPoint struct {
	Period        string `json:"period"`
	Succeeded     int32  `json:"succeeded"`
	FailedChanges int32  `json:"failed_changes"`
}

// ProjectDora is one project's changes in the range.
type ProjectDora struct {
	ID                uuid.UUID `json:"id"`
	Name              string    `json:"name"`
	Slug              string    `json:"slug"`
	Changes           int32     `json:"changes"`
	ChangeFailureRate *float64  `json:"change_failure_rate"`
	LeadTimeMedian    *float64  `json:"lead_time_median_s"`
	LastDeployedAt    time.Time `json:"last_deployed_at"`
}

// Dora is GET /orgs/{id}/dashboard/dora.
type Dora struct {
	Range             Range             `json:"range"`
	Deployments       Deployments       `json:"deployments"`
	ChangeFailureRate ChangeFailureRate `json:"change_failure_rate"`
	LeadTime          LeadTime          `json:"lead_time"`
	TimeToRestore     TimeToRestore     `json:"time_to_restore"`
	Alerts            AlertRecovery     `json:"alerts"`
	Trend             []DeploymentPoint `json:"trend"`
	Projects          []ProjectDora     `json:"projects"`
}

// Dora returns the four DORA metrics for production (or one environment) over the projects
// the caller can see, plus the organization's alert recovery.
func (s *Service) Dora(ctx context.Context, orgID uuid.UUID, in Query) (Dora, error) {
	q := store.New(s.pool)
	sc, err := s.resolve(ctx, q, orgID, in)
	if err != nil {
		return Dora{}, err
	}
	sum, err := q.DoraSummary(ctx, store.DoraSummaryParams{
		OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To, EnvironmentID: sc.environment,
		SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return Dora{}, err
	}
	rest, err := q.DoraRestores(ctx, store.DoraRestoresParams{
		OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To, EnvironmentID: sc.environment,
		SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return Dora{}, err
	}
	alerts, err := q.AlertRestores(ctx, store.AlertRestoresParams{OrganizationID: orgID, FromTs: &sc.rng.From, ToTs: &sc.rng.To})
	if err != nil {
		return Dora{}, err
	}
	trend, err := q.DeploymentTrend(ctx, store.DeploymentTrendParams{
		Bucket: sc.rng.Bucket, Tz: sc.rng.TZ, OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To,
		EnvironmentID: sc.environment, SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return Dora{}, err
	}
	byProject, err := q.DoraByProject(ctx, store.DoraByProjectParams{
		OrganizationID: orgID, FromTs: sc.rng.From, ToTs: sc.rng.To, EnvironmentID: sc.environment,
		SeeAll: sc.seeAll, ProjectIds: sc.projectIDs, ProjectID: sc.project,
	})
	if err != nil {
		return Dora{}, err
	}
	days := sc.rng.To.Sub(sc.rng.From).Hours() / 24
	out := Dora{
		Range:             sc.rng,
		Deployments:       Deployments{Succeeded: sum.Succeeded, PerDay: float64(sum.Succeeded) / days},
		ChangeFailureRate: ChangeFailureRate{Failed: sum.FailedChanges, Changes: sum.Changes, Rate: ratio(sum.FailedChanges, sum.Changes)},
		LeadTime:          LeadTime{Median: seconds(sum.LeadP50), P95: seconds(sum.LeadP95), Samples: sum.LeadSamples},
		TimeToRestore:     TimeToRestore{Median: seconds(rest.RestoreP50), Restored: rest.Restored, Open: rest.Open},
		Alerts:            AlertRecovery{Resolved: alerts.Resolved, Median: seconds(alerts.ResolveP50)},
		Trend:             []DeploymentPoint{},
		Projects:          make([]ProjectDora, 0, len(byProject)),
	}
	got := map[string]store.DeploymentTrendRow{}
	for _, t := range trend {
		got[t.Period] = t
	}
	for _, p := range periods(sc.rng, sc.loc) {
		t := got[p]
		out.Trend = append(out.Trend, DeploymentPoint{Period: p, Succeeded: t.Succeeded, FailedChanges: t.FailedChanges})
	}
	for _, p := range byProject {
		out.Projects = append(out.Projects, ProjectDora{
			ID: p.ID, Name: p.Name, Slug: p.Slug, Changes: p.Changes, ChangeFailureRate: ratio(p.FailedChanges, p.Changes),
			LeadTimeMedian: seconds(p.LeadP50), LastDeployedAt: p.LastDeployedAt,
		})
	}
	return out, nil
}
