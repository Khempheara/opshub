package infra

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// AgentToken is shown once when issued.
type AgentToken struct {
	AssetID     uuid.UUID `json:"asset_id"`
	Token       string    `json:"token"`
	TokenPrefix string    `json:"token_prefix"`
}

// IssueAgentToken issues (or rotates, revoking the previous one) a server's agent token
// (infra.manage).
func (s *Service) IssueAgentToken(ctx context.Context, id uuid.UUID) (AgentToken, error) {
	var out AgentToken
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		a, err := s.loadAsset(ctx, q, id, authz.InfraManage)
		if err != nil {
			return err
		}
		if a.Kind != store.AssetKindServer {
			return apperr.New(apperr.CodeAgentNotSupported, http.StatusUnprocessableEntity, "only servers run the infra agent")
		}
		prefix := strings.ToLower(crypto.RandomBase32(8))
		token := authn.AgentTokenPrefix + prefix + "_" + crypto.RandomToken(32)
		if _, err := q.SetAgentToken(ctx, store.SetAgentTokenParams{ID: id, TokenHash: crypto.HashToken(token), TokenPrefix: authn.AgentTokenPrefix + prefix}); err != nil {
			return err
		}
		out = AgentToken{AssetID: id, Token: token, TokenPrefix: authn.AgentTokenPrefix + prefix}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &a.OrganizationID, Action: "asset.agent_token", ResourceType: "asset", ResourceID: id.String(),
			Metadata: map[string]any{"rotated": a.AgentTokenHash != nil, "token_prefix": out.TokenPrefix},
		})
	})
	return out, err
}

func errAgentToken() *apperr.Error {
	return apperr.New(apperr.CodeAgentTokenInvalid, http.StatusUnauthorized, "agent token is invalid or was rotated")
}

// AuthenticateAgent resolves an agent token to its server.
func (s *Service) AuthenticateAgent(ctx context.Context, token string) (store.InfraAsset, error) {
	if !strings.HasPrefix(token, authn.AgentTokenPrefix) {
		return store.InfraAsset{}, errAgentToken()
	}
	a, err := store.New(s.pool).GetAssetByAgentToken(ctx, crypto.HashToken(token))
	if database.IsNoRows(err) {
		return a, errAgentToken()
	}
	return a, err
}

// Heartbeat is POST /agent/heartbeat. Percentages are 0-100; omitted values are unknown
// (e.g. an agent on an unsupported OS).
type Heartbeat struct {
	Version       string   `json:"version" validate:"max=50"`
	Hostname      string   `json:"hostname" validate:"max=255"`
	OS            string   `json:"os" validate:"max=50"`
	Arch          string   `json:"arch" validate:"max=50"`
	UptimeSeconds int64    `json:"uptime_seconds" validate:"min=0"`
	CPU           *float32 `json:"cpu_pct" validate:"omitempty,min=0,max=100"`
	Mem           *float32 `json:"mem_pct" validate:"omitempty,min=0,max=100"`
	Disk          *float32 `json:"disk_pct" validate:"omitempty,min=0,max=100"`
	Load          *float32 `json:"load1" validate:"omitempty,min=0,max=100000"`
}

// HeartbeatResult tells the agent when to report next.
type HeartbeatResult struct {
	IntervalSeconds int `json:"interval_seconds"`
}

// RecordHeartbeat stores a sample (timestamped by the server, not the agent's clock).
func (s *Service) RecordHeartbeat(ctx context.Context, a store.InfraAsset, hb Heartbeat) (HeartbeatResult, error) {
	latest, _ := json.Marshal(Metrics{CPU: hb.CPU, Mem: hb.Mem, Disk: hb.Disk, Load: hb.Load})
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		if err := q.RecordHeartbeat(ctx, store.RecordHeartbeatParams{
			ID: a.ID, AgentVersion: hb.Version, AgentHostname: hb.Hostname, AgentOs: hb.OS, AgentArch: hb.Arch, LastMetrics: latest,
		}); err != nil {
			return err
		}
		if hb.CPU == nil && hb.Mem == nil && hb.Disk == nil && hb.Load == nil {
			return nil
		}
		return q.InsertMetric(ctx, store.InsertMetricParams{AssetID: a.ID, CpuPct: hb.CPU, MemPct: hb.Mem, DiskPct: hb.Disk, Load1: hb.Load})
	})
	return HeartbeatResult{IntervalSeconds: int(HeartbeatInterval.Seconds())}, err
}
