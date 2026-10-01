package seed

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/store"
)

// showcaseMobilePipeline is mobile-app's .opshub.yml: test, build, an automatic staging
// deploy and a production deploy that needs two approvals (see the protection rule).
const showcaseMobilePipeline = `version: 1
on:
  push:
    branches: [main, "feature/*", "release/*"]
    tags: ["v*"]
  pull_request:
    branches: [main]
stages: [test, build, deploy]
jobs:
  lint:
    stage: test
    image: golangci/golangci-lint:v2
    steps:
      - golangci-lint run
  test:
    stage: test
    image: golang:1.26
    steps:
      - go test -race ./...
  build:
    stage: build
    needs: [lint, test]
    image: docker:27
    steps:
      - docker build -t mobile-api:${OPSHUB_COMMIT_SHA} .
  deploy-staging:
    stage: deploy
    needs: [build]
    environment: staging
    image: alpine:3
    steps:
      - ./scripts/deploy.sh staging
  deploy-production:
    stage: deploy
    needs: [deploy-staging]
    environment: production
    when: manual
    image: alpine:3
    steps:
      - ./scripts/deploy.sh production
`

// showcaseDataPipeline is data-pipeline's .opshub.yml (it also runs nightly on a schedule).
const showcaseDataPipeline = `version: 1
on:
  push:
    branches: [main]
stages: [check, import, report]
jobs:
  check:
    stage: check
    image: python:3.13
    steps:
      - ruff check .
      - pytest -q
  import:
    stage: import
    needs: [check]
    image: python:3.13
    steps:
      - python -m pipeline.import --since yesterday
  report:
    stage: report
    needs: [import]
    image: python:3.13
    steps:
      - python -m pipeline.report --email finance
`

var showcaseJobLogs = map[string]string{
	"lint":              dim + "$ golangci-lint run" + reset + "\n0 issues.\n",
	"test":              dim + "$ go test -race ./..." + reset + "\nok  \tmobile/api/internal/auth\t0.812s\nok  \tmobile/api/internal/push\t1.204s\n" + green + "PASS" + reset + " 389 tests\n",
	"test-failed":       dim + "$ go test -race ./..." + reset + "\n--- " + red + "FAIL" + reset + ": TestOfflineQueueReplay (0.03s)\n    queue_test.go:88: replayed 2 of 3 queued orders\n" + red + "FAIL" + reset + "\tmobile/api/internal/offline\t0.402s\n",
	"build":             dim + "$ docker build ." + reset + "\n#8 [builder 3/3] RUN go build -o /out/api ./cmd/api\n" + green + "Successfully built mobile-api" + reset + "\n",
	"deploy-staging":    dim + "$ ./scripts/deploy.sh staging" + reset + "\nSwitching staging to the green slot…\n" + green + "Healthy" + reset + "\n",
	"deploy-production": dim + "$ ./scripts/deploy.sh production" + reset + "\nSwitching production to the blue slot…\n" + green + "Healthy" + reset + "\n",
	"check":             dim + "$ ruff check ." + reset + "\nAll checks passed!\n" + dim + "$ pytest -q" + reset + "\n" + green + "142 passed" + reset + " in 9.81s\n",
	"import":            dim + "$ python -m pipeline.import --since yesterday" + reset + "\nImported 18,240 orders from 31 stores\n",
	"import-failed":     dim + "$ python -m pipeline.import --since yesterday" + reset + "\n" + red + "psycopg.OperationalError: connection to server at \"orders-db\" timed out" + reset + "\n",
	"report":            dim + "$ python -m pipeline.report --email finance" + reset + "\nSent the daily sales report (KHR and USD) to finance@mekong.example\n",
}

// runnersAndTargets registers runners in every state, an unused registration token, and one
// deploy target of each kind (all unreachable on purpose: deployments fail safely).
func (s *sc) runnersAndTargets() error {
	ctx := s.ctx
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		admin := s.user("admin@demo.opshub.local")
		for _, r := range []struct {
			name, version, os, arch string
			labels                  []string
			conc                    int
			seen                    *time.Time
			disabled                *time.Time
		}{
			{"linux-builder-01", "1.4.0", "linux", "amd64", []string{"linux", "amd64", "docker"}, 4, at(s.now.Add(-20 * time.Second)), nil},
			{"arm-builder-01", "1.4.0", "linux", "arm64", []string{"linux", "arm64", "docker"}, 2, at(s.now.Add(-3 * time.Hour)), nil},
			{"mac-mini-ios", "1.3.2", "darwin", "arm64", []string{"macos", "xcode"}, 1, at(s.now.Add(-2 * time.Minute)), nil},
			{"old-builder", "1.1.0", "linux", "amd64", []string{"linux"}, 1, at(s.now.AddDate(0, 0, -12)), at(s.now.AddDate(0, 0, -10))},
		} {
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO runners (organization_id, name, labels, token_hash, token_prefix, version, os, arch,
				max_concurrency, last_seen_at, disabled_at, created_by, created_at)
				VALUES ($1, $2, $3, $4, 'ohr_demo', $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
				s.org, r.name, r.labels, crypto.HashToken(crypto.RandomToken(32)), r.version, r.os, r.arch, r.conc, r.seen, r.disabled,
				admin, s.now.AddDate(0, 0, -38)).Scan(&id); err != nil {
				return err
			}
			s.runners[r.name] = id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runner_registration_tokens (organization_id, token_hash, labels, created_by, expires_at)
			VALUES ($1, $2, '{linux,gpu}', $3, $4)`, s.org, crypto.HashToken(crypto.RandomToken(32)), admin, s.now.Add(20*time.Hour)); err != nil {
			return err
		}

		kubeconfig, err := demoKubeconfig()
		if err != nil {
			return err
		}
		health := map[string]any{"url": "http://localhost:8080/healthz", "expect_status": 200, "timeout_seconds": 5, "retries": 3}
		for _, t := range []struct {
			name, kind, description string
			config, creds           map[string]any
			tested                  *time.Time
			ok                      *bool
		}{
			{"mekong-k8s", "kubernetes", "Production cluster, blue/green · ចង្កោមផលិតកម្ម",
				map[string]any{"namespace": "mobile", "deployment": "mobile-api", "service": "mobile-api", "rollout_timeout_seconds": 600},
				map[string]any{"kubeconfig": kubeconfig}, at(s.now.Add(-26 * time.Hour)), ptr(true)},
			{"edge-ssh", "ssh", "Two edge VMs, one at a time",
				map[string]any{"hosts": []string{"edge-1.mekong.invalid", "edge-2.mekong.invalid:2222"}, "user": "deploy",
					"command": "sudo /opt/data-api/deploy.sh", "batch_size": 1, "health_check": health},
				map[string]any{"private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\ndemo-not-a-real-key\n-----END OPENSSH PRIVATE KEY-----\n"},
				at(s.now.Add(-3 * time.Hour)), ptr(false)},
			{"reports-docker", "docker", "Docker host for the report workers",
				map[string]any{"connection": "ssh", "host": "docker-1.mekong.invalid", "user": "deploy", "container": "reports",
					"replicas": 2, "env": map[string]string{"TZ": "Asia/Phnom_Penh"}, "restart": "unless-stopped"},
				map[string]any{"private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\ndemo-not-a-real-key\n-----END OPENSSH PRIVATE KEY-----\n",
					"registry_server": "ghcr.io", "registry_username": "mekong-bot", "registry_password": "demo-not-real"},
				nil, nil},
		} {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			enc, err := s.keys.Encrypt(mustJSON(t.creds), id[:])
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO deploy_targets (id, organization_id, name, kind, description, config, credentials_enc,
				last_test_at, last_test_ok, created_by, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, s.org, t.name, t.kind, t.description, mustJSON(t.config), enc,
				t.tested, t.ok, admin, s.now.AddDate(0, 0, -37)); err != nil {
				return err
			}
			s.targets[t.name] = id
		}
		return nil
	})
}

// history gives mobile-app 30 days of runs and staging/production deployments, so the
// dashboard's DORA cards and charts are filled. They take the first run and deployment numbers.
func (s *sc) history() error {
	ctx := s.ctx
	rng := rand.New(rand.NewPCG(2026, 12)) // #nosec G404 -- demo data, fixed for the same history every time
	project := s.projects["mobile-app"]
	staging, prod := s.envs["mobile-app"]["staging"], s.envs["mobile-app"]["production"]
	authors := []string{"bopha-ly", "rithy-chea", "vicheka-sok", "chantha-ros"}
	titles := []string{"Add push notification settings", "Fix login on Android 15", "Show prices in riel", "Speed up the order list",
		"Support Khmer keyboard search", "Retry failed uploads", "Update the payment SDK", "Cache the product catalogue"}
	runs, deploys, version := 0, 0, 100
	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		admin := s.user("admin@demo.opshub.local")
		deploy := func(env uuid.UUID, run *uuid.UUID, status string, reverted bool, rollbackOf *uuid.UUID, created time.Time) (uuid.UUID, error) {
			deploys++
			var id uuid.UUID
			var reason *string
			if status == "failed" {
				reason = ptr("health_check_failed")
			}
			err := tx.QueryRow(ctx, `INSERT INTO deployments (organization_id, project_id, environment_id, number, target_id, target_name, target_kind,
				version, strategy, status, failure_reason, reverted, run_id, rollback_of_id, created_by, created_at, started_at, finished_at)
				VALUES ($1, $2, $3, $4, $5, 'mekong-k8s', 'kubernetes', $6, 'blue_green', $7, $8, $9, $10, $11, $12, $13, $13, $14) RETURNING id`,
				s.org, project, env, deploys, s.targets["mekong-k8s"], fmt.Sprintf("ghcr.io/mekong-cloud/mobile-api:2.%d.0", version-100),
				status, reason, reverted, run, rollbackOf, admin, created, created.Add(150*time.Second)).Scan(&id)
			return id, err
		}
		var current, currentStaging uuid.UUID
		for day := 30; day >= 1; day-- {
			start := s.now.AddDate(0, 0, -day).Truncate(24 * time.Hour).Add(2 * time.Hour)
			weekend := start.Weekday() == time.Saturday || start.Weekday() == time.Sunday
			n := 1 + rng.IntN(4)
			if weekend {
				n = rng.IntN(2)
			}
			for i := range n {
				runs++
				created := start.Add(time.Duration(i*100+rng.IntN(60)) * time.Minute)
				took := time.Duration(240+rng.IntN(420)) * time.Second
				status := "succeeded"
				if r := rng.IntN(100); r < 14 {
					status = "failed"
				} else if r < 17 {
					status = "canceled"
				}
				var runID uuid.UUID
				if err := tx.QueryRow(ctx, `INSERT INTO pipeline_runs (organization_id, project_id, number, status, trigger, ref, commit_sha,
					title, actor_name, created_at, started_at, finished_at, committed_at)
					VALUES ($1, $2, $3, $4, 'push', 'refs/heads/main', $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
					s.org, project, runs, status, fmt.Sprintf("%012x", rng.Uint64()>>16), titles[rng.IntN(len(titles))],
					authors[rng.IntN(len(authors))], created, created.Add(15*time.Second), created.Add(took),
					created.Add(-time.Duration(30+rng.IntN(300))*time.Minute)).Scan(&runID); err != nil {
					return err
				}
				if status != "succeeded" || weekend || rng.IntN(3) == 0 {
					continue
				}
				version++
				at := created.Add(took + 2*time.Minute)
				id, err := deploy(staging, &runID, "succeeded", false, nil, at)
				if err != nil {
					return err
				}
				currentStaging = id
				at = at.Add(time.Duration(30+rng.IntN(180)) * time.Minute)
				switch r := rng.IntN(100); {
				case r < 10:
					if _, err := deploy(prod, &runID, "failed", true, nil, at); err != nil {
						return err
					}
				case r < 15:
					bad, err := deploy(prod, &runID, "succeeded", false, nil, at)
					if err != nil {
						return err
					}
					if current, err = deploy(prod, nil, "succeeded", false, &bad, at.Add(20*time.Minute)); err != nil {
						return err
					}
				default:
					if current, err = deploy(prod, &runID, "succeeded", false, nil, at); err != nil {
						return err
					}
				}
			}
		}
		for env, id := range map[uuid.UUID]uuid.UUID{prod: current, staging: currentStaging} {
			if id != uuid.Nil {
				if _, err := tx.Exec(ctx, "UPDATE environments SET current_deployment_id = $1 WHERE id = $2", id, env); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, "UPDATE projects SET last_run_number = $2, last_deployment_number = $3 WHERE id = $1", project, runs, deploys)
		return err
	})
	if err == nil {
		_, _ = fmt.Fprintf(s.out, "seed: showcase history: %d runs and %d deployments over 30 days\n", runs, deploys)
	}
	return err
}

// runs adds recent runs in every status and trigger through the pipeline service, executed by
// the linux-builder-01 runner, then moves them back in time so they read as recent history.
// The last two stay running and queued (with no live runner they later fail as "runner
// lost" / "no runner", which shows those states too).
func (s *sc) runs() error {
	ctx := s.ctx
	svc := pipeline.NewService(s.pool, nil, slog.New(slog.DiscardHandler))
	as := func(email string) authn.Principal {
		return authn.Principal{Kind: authn.KindSession, UserID: s.user(email), SessionID: uuid.New()}
	}
	mobileDef, err := spec.Parse([]byte(showcaseMobilePipeline))
	if err != nil {
		return err
	}
	dataDef, err := spec.Parse([]byte(showcaseDataPipeline))
	if err != nil {
		return err
	}
	mobile, data := s.projects["mobile-app"], s.projects["data-pipeline"]
	runner := s.runners["linux-builder-01"]

	// work runs every queued job; jobs named in fail fail with their "-failed" log.
	work := func(fail ...string) error {
		for {
			c, err := svc.Claim(ctx, s.org, runner, nil, nil)
			if err != nil || c == nil {
				return err
			}
			failing := false
			for _, f := range fail {
				failing = failing || f == c.Job.Name
			}
			log := showcaseJobLogs[c.Job.Name]
			if failing {
				log = showcaseJobLogs[c.Job.Name+"-failed"]
			}
			for i := range c.Job.Steps {
				if err := svc.ReportStep(ctx, c.Job.ID, int32(i), store.StepStatusRunning, nil); err != nil { // #nosec G115 -- few steps
					return err
				}
			}
			if err := svc.AppendLog(ctx, c.Job.ID, 0, log, nil); err != nil {
				return err
			}
			var code *int32
			if failing {
				code = ptr(int32(1))
			}
			if err := svc.Complete(ctx, c.Job.ID, !failing, code, ""); err != nil {
				return err
			}
		}
	}
	decide := func(run uuid.UUID, email string, d store.ApprovalDecision, comment string) error {
		var job uuid.UUID
		if err := s.pool.QueryRow(ctx, "SELECT id FROM pipeline_jobs WHERE run_id = $1 AND status = 'waiting_approval'", run).Scan(&job); err != nil {
			return err
		}
		_, err := svc.Decide(authn.WithPrincipal(ctx, as(email)), job, pipeline.DecideInput{Decision: d, Comment: comment})
		return err
	}
	remember := func(key string, run uuid.UUID) error {
		var n int32
		err := s.pool.QueryRow(ctx, "SELECT number FROM pipeline_runs WHERE id = $1", run).Scan(&n)
		s.numbers[key] = n
		return err
	}
	type aged struct {
		id  uuid.UUID
		ago time.Duration
	}
	var past []aged
	create := func(project uuid.UUID, def *spec.Definition, trigger store.RunTrigger, ref, sha, title, actor string, by *uuid.UUID) (uuid.UUID, error) {
		return svc.CreateRunFromDefinition(ctx, project, def, trigger, ref, sha, title, actor, by)
	}

	// Released to production with two approvals.
	r, err := create(mobile, mobileDef, store.RunTriggerPush, "refs/heads/main", "9a8b7c6d5e4f", "Show prices in riel", "bopha-ly", nil)
	if err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	if err := decide(r, "owner@demo.opshub.local", store.ApprovalDecisionApproved, "Release notes checked · បានពិនិត្យ"); err != nil {
		return err
	}
	if err := decide(r, ShowcaseTwoFactorEmail, store.ApprovalDecisionApproved, "Go · បន្ត"); err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	if err := remember("released", r); err != nil {
		return err
	}
	past = append(past, aged{r, 30 * time.Hour})

	// A tag whose production deploy was rejected.
	if r, err = create(mobile, mobileDef, store.RunTriggerTag, "refs/tags/v2.3.0", "1f2e3d4c5b6a", "Release v2.3.0", "rithy-chea", nil); err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	if err := decide(r, "admin@demo.opshub.local", store.ApprovalDecisionRejected, "Not during the sales campaign · មិនមែនពេលយុទ្ធនាការលក់ទេ"); err != nil {
		return err
	}
	if err := remember("tag", r); err != nil {
		return err
	}
	past = append(past, aged{r, 26 * time.Hour})

	// A pull request: checks pass, and protected production refuses its ref ("branch not allowed").
	if r, err = create(mobile, mobileDef, store.RunTriggerPullRequest, "refs/pull/42/head", "77aa88bb99cc", "Retry failed uploads (#42)", "chantha-ros", nil); err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	past = append(past, aged{r, 20 * time.Hour})

	// A failing release branch, then a re-run that passes (its production deploy is then canceled).
	if r, err = create(mobile, mobileDef, store.RunTriggerPush, "refs/heads/release/2.4", "e5d4c3b2a190", "Queue orders while offline", "vicheka-sok", nil); err != nil {
		return err
	}
	if err := work("test"); err != nil {
		return err
	}
	if err := remember("failed", r); err != nil {
		return err
	}
	past = append(past, aged{r, 9 * time.Hour})
	rr, err := svc.Rerun(authn.WithPrincipal(ctx, as("dev@demo.opshub.local")), r, false) // a new run: "re-run of #N"
	if err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	if err := cancelWaiting(s, svc, rr.ID); err != nil {
		return err
	}
	if err := remember("rerun", rr.ID); err != nil {
		return err
	}
	past = append(past, aged{rr.ID, 8 * time.Hour})

	// Canceled while testing.
	if r, err = create(mobile, mobileDef, store.RunTriggerPush, "refs/heads/main", "0badc0ffee12", "Try the new image cache", "rithy-chea", nil); err != nil {
		return err
	}
	if c, err := svc.Claim(ctx, s.org, runner, nil, nil); err != nil || c == nil {
		return fmt.Errorf("claim: %w", err)
	}
	if _, err := svc.CancelRun(authn.WithPrincipal(ctx, as("rithy@demo.opshub.local")), r); err != nil {
		return err
	}
	if err := remember("canceled", r); err != nil {
		return err
	}
	past = append(past, aged{r, 5 * time.Hour})

	// Waiting for production approval (one of two given).
	dev := s.user("dev@demo.opshub.local")
	if r, err = create(mobile, mobileDef, store.RunTriggerManual, "refs/heads/main", "9a8b7c6d5e4f", "Show prices in riel", "", &dev); err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	if err := decide(r, "owner@demo.opshub.local", store.ApprovalDecisionApproved, "One more approval needed · ត្រូវការការអនុម័តមួយទៀត"); err != nil {
		return err
	}
	past = append(past, aged{r, 40 * time.Minute})

	// data-pipeline: nightly scheduled runs (one failed import) and a push.
	for i, fail := range []string{"", "import", ""} {
		if r, err = create(data, dataDef, store.RunTriggerSchedule, "refs/heads/main", "5c4b3a291807", "Nightly import", "", nil); err != nil {
			return err
		}
		if fail == "" {
			err = work()
		} else {
			err = work(fail)
		}
		if err != nil {
			return err
		}
		past = append(past, aged{r, time.Duration(3-i)*24*time.Hour - 5*time.Hour})
	}
	if r, err = create(data, dataDef, store.RunTriggerPush, "refs/heads/main", "6d5c4b3a2918", "Add the Battambang stores", "bopha-ly", nil); err != nil {
		return err
	}
	if err := work(); err != nil {
		return err
	}
	past = append(past, aged{r, 3 * time.Hour})

	// Move the finished runs back in time (newest stays newest) and give their jobs believable
	// durations: they ran instantly here. Each stage starts when the one before it ended.
	for _, p := range past {
		if _, err := s.pool.Exec(ctx, `UPDATE pipeline_jobs SET created_at = created_at - $2::interval, queued_at = queued_at - $2::interval,
				started_at = started_at - $2::interval + stage_index * interval '150 seconds',
				finished_at = finished_at - $2::interval + stage_index * interval '150 seconds' + interval '95 seconds' + (length(name) % 5) * interval '9 seconds'
			WHERE run_id = $1`, p.id, p.ago); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE job_steps st SET started_at = j.started_at, finished_at = j.finished_at
			FROM pipeline_jobs j WHERE j.id = st.job_id AND j.run_id = $1 AND st.started_at IS NOT NULL`, p.id); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE job_approvals a SET created_at = a.created_at - $2::interval + interval '6 minutes'
			FROM pipeline_jobs j WHERE j.id = a.job_id AND j.run_id = $1`, p.id, p.ago); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE pipeline_runs r SET created_at = r.created_at - $2::interval, started_at = r.started_at - $2::interval,
				finished_at = CASE WHEN r.finished_at IS NOT NULL THEN (SELECT max(finished_at) FROM pipeline_jobs WHERE run_id = r.id) END, -- waiting runs stay open
				committed_at = r.created_at - $2::interval - interval '40 minutes'
			WHERE r.id = $1`, p.id, p.ago); err != nil {
			return err
		}
	}

	// Running now, and queued behind it.
	if _, err = create(mobile, mobileDef, store.RunTriggerPush, "refs/heads/main", "abcdef012345", "Support Khmer keyboard search", "bopha-ly", nil); err != nil {
		return err
	}
	c, err := svc.Claim(ctx, s.org, runner, nil, nil)
	if err != nil || c == nil {
		return fmt.Errorf("claim: %w", err)
	}
	if err := svc.ReportStep(ctx, c.Job.ID, 0, store.StepStatusRunning, nil); err != nil {
		return err
	}
	if err := svc.AppendLog(ctx, c.Job.ID, 0, dim+"$ "+c.Spec.Steps[0].Run+reset+"\n", nil); err != nil {
		return err
	}
	if _, err := create(data, dataDef, store.RunTriggerManual, "refs/heads/main", "6d5c4b3a2918", "Re-import September", "", &dev); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(s.out, "seed: showcase runs: every status and trigger, a re-run, approvals given and rejected\n")
	return nil
}

// cancelWaiting cancels a run that stopped at a production approval, as someone would after
// the re-run proved the fix (it keeps the waiting list to the one run meant to wait).
func cancelWaiting(s *sc, svc *pipeline.Service, run uuid.UUID) error {
	p := authn.Principal{Kind: authn.KindSession, UserID: s.user("dev@demo.opshub.local"), SessionID: uuid.New()}
	_, err := svc.CancelRun(authn.WithPrincipal(s.ctx, p), run)
	return err
}

// deployments adds recent deployments on all three targets: a failed release that reverted
// itself, a rollback, and failed SSH and Docker deployments with their logs.
func (s *sc) deployments() error {
	ctx := s.ctx
	type dep struct {
		project, env, target, kind, strategy, version, previous, status, reason string
		reverted, live                                                          bool
		ago                                                                     time.Duration
		log                                                                     string
	}
	ok := func(v, env, target string) string {
		return "Deploying " + v + " to " + env + " on " + target + "\n\x1b[32;1mDeployed " + v + " to " + target + ".\x1b[0m\n"
	}
	list := []dep{
		{project: "mobile-app", env: "staging", target: "mekong-k8s", kind: "kubernetes", strategy: "blue_green",
			version: "ghcr.io/mekong-cloud/mobile-api:2.40.0-rc1", previous: "ghcr.io/mekong-cloud/mobile-api:2.39.0", status: "failed",
			reason: "health_check_failed", reverted: true, ago: 4 * time.Hour,
			log: "Deploying ghcr.io/mekong-cloud/mobile-api:2.40.0-rc1 to staging on mekong-k8s (kubernetes, blue_green)\nStarting the green slot…\n" +
				"\x1b[31;1mHealth check failed: GET /healthz returned 503 three times\x1b[0m\nTraffic stays on the blue slot (2.39.0).\n"},
		{project: "data-pipeline", env: "staging", target: "edge-ssh", kind: "ssh", strategy: "rolling",
			version: "ghcr.io/mekong-cloud/data-api:0.9.1", previous: "ghcr.io/mekong-cloud/data-api:0.9.0", status: "succeeded",
			ago: 6 * 24 * time.Hour, live: false,
			log: ok("ghcr.io/mekong-cloud/data-api:0.9.1", "staging", "edge-ssh") + "edge-1.mekong.invalid: healthy\nedge-2.mekong.invalid:2222: healthy\n"},
		{project: "data-pipeline", env: "production", target: "reports-docker", kind: "docker", strategy: "rolling",
			version: "ghcr.io/mekong-cloud/reports:3.2.0", status: "succeeded", ago: 5 * 24 * time.Hour, live: true,
			log: ok("ghcr.io/mekong-cloud/reports:3.2.0", "production", "reports-docker") + "reports-1, reports-2 running\n"},
		{project: "data-pipeline", env: "production", target: "reports-docker", kind: "docker", strategy: "rolling",
			version: "ghcr.io/mekong-cloud/reports:3.3.0", previous: "ghcr.io/mekong-cloud/reports:3.2.0", status: "failed",
			reason: "target_unreachable", ago: 2 * time.Hour,
			log: "Deploying ghcr.io/mekong-cloud/reports:3.3.0 to production on reports-docker (docker, rolling)\n" +
				"\x1b[31;1mCan't reach docker-1.mekong.invalid:22: no such host\x1b[0m\nNothing was changed.\n"},
		{project: "data-pipeline", env: "staging", target: "edge-ssh", kind: "ssh", strategy: "rolling",
			version: "ghcr.io/mekong-cloud/data-api:0.10.0", previous: "ghcr.io/mekong-cloud/data-api:0.9.1", status: "succeeded",
			ago: 50 * time.Minute, live: true,
			log: ok("ghcr.io/mekong-cloud/data-api:0.10.0", "staging", "edge-ssh") + "edge-1.mekong.invalid: healthy\nedge-2.mekong.invalid:2222: healthy\n"},
	}
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		by := s.user(ShowcaseTwoFactorEmail)
		okHealth := `[{"target":"GET /healthz","ok":true,"attempts":1,"detail":"200 in 31 ms","at":"2026-01-01T00:00:00Z"}]`
		badHealth := `[{"target":"GET /healthz","ok":false,"attempts":3,"detail":"503 Service Unavailable","at":"2026-01-01T00:00:00Z"}]`
		for _, d := range list {
			project := s.projects[d.project]
			var n int32
			if err := tx.QueryRow(ctx, "UPDATE projects SET last_deployment_number = last_deployment_number + 1 WHERE id = $1 RETURNING last_deployment_number",
				project).Scan(&n); err != nil {
				return err
			}
			var reason *string
			if d.reason != "" {
				reason = &d.reason
			}
			health := okHealth
			if d.reason == "health_check_failed" {
				health = badHealth
			} else if d.status == "failed" {
				health = "[]"
			}
			created := s.now.Add(-d.ago)
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO deployments (organization_id, project_id, environment_id, number, target_id, target_name,
				target_kind, version, previous_version, strategy, status, failure_reason, reverted, health, created_by, started_at, finished_at,
				created_at, log_bytes)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $16, $18) RETURNING id`,
				s.org, project, s.envs[d.project][d.env], n, s.targets[d.target], d.target, d.kind, d.version, d.previous, d.strategy, d.status,
				reason, d.reverted, health, by, created, created.Add(70*time.Second), len(d.log)).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO deployment_log_chunks (deployment_id, seq, content) VALUES ($1, 0, $2)", id, d.log); err != nil {
				return err
			}
			s.numbers[d.version] = n
			if d.live {
				if _, err := tx.Exec(ctx, "UPDATE environments SET current_deployment_id = $1 WHERE id = $2", id, s.envs[d.project][d.env]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// secrets adds project-wide and per-environment secrets, one rotated twice (three versions).
func (s *sc) secrets() error {
	svc := secret.NewService(s.pool, s.keys, slog.New(slog.DiscardHandler))
	actx := authn.WithPrincipal(s.ctx, authn.Principal{Kind: authn.KindSession, UserID: s.user("admin@demo.opshub.local"), SessionID: uuid.New()})
	env := func(project, name string) *uuid.UUID {
		id := s.envs[project][name]
		return &id
	}
	list := []struct {
		project string
		in      secret.CreateInput
		rotate  int
	}{
		{"mobile-app", secret.CreateInput{Name: "FIREBASE_SERVICE_ACCOUNT", Description: "Push notifications · ការជូនដំណឹង", Value: `{"type":"service_account","demo":true}`}, 0},
		{"mobile-app", secret.CreateInput{Name: "PAYMENT_API_KEY", EnvironmentID: env("mobile-app", "staging"), Description: "Sandbox key", Value: "sk_test_demo"}, 0},
		{"mobile-app", secret.CreateInput{Name: "PAYMENT_API_KEY", EnvironmentID: env("mobile-app", "production"), Description: "Live key, rotated monthly", Value: "sk_live_demo_1"}, 2},
		{"mobile-app", secret.CreateInput{Name: "APNS_KEY", EnvironmentID: env("mobile-app", "production"), Description: "Apple push key", Value: "demo-apns-key"}, 0},
		{"data-pipeline", secret.CreateInput{Name: "WAREHOUSE_URL", EnvironmentID: env("data-pipeline", "production"), Description: "Reporting warehouse", Value: "postgres://reports:demo@warehouse.internal/sales"}, 1}, // #nosec G101 -- a placeholder for the demo
		{"data-pipeline", secret.CreateInput{Name: "SMTP_PASSWORD", Description: "Sends the daily report", Value: "demo-smtp"}, 0},
	}
	for _, l := range list {
		sec, err := svc.Create(actx, s.projects[l.project], l.in)
		if err != nil {
			return fmt.Errorf("secret %s: %w", l.in.Name, err)
		}
		for i := range l.rotate {
			if _, err := svc.Rotate(actx, sec.ID, secret.RotateInput{Value: fmt.Sprintf("%s_v%d", l.in.Value, i+2)}); err != nil {
				return err
			}
		}
	}
	return nil
}
