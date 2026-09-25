package infra

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/store"
)

// Handler exposes the inventory and the agent endpoint.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type ctxKey int

const assetKey ctxKey = 0

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/assets", h.list)
		r.Post("/orgs/{orgId}/assets", h.create)
		r.Get("/orgs/{orgId}/certificates", h.certificates)
		r.Get("/assets/{assetId}", h.get)
		r.Patch("/assets/{assetId}", h.update)
		r.Delete("/assets/{assetId}", h.delete)
		r.Get("/assets/{assetId}/metrics", h.metrics)
		r.Post("/assets/{assetId}/agent-token", h.agentToken)
		r.Get("/assets/{assetId}/certificate", h.certificate)
		r.Post("/assets/{assetId}/certificate/check", h.checkCertificate)
	})
	r.With(h.agentAuth).Post("/agent/heartbeat", h.heartbeat)
}

func pathID(r *http.Request, name string, notFound func() *apperr.Error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, notFound()
	}
	return id, nil
}

func orgID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
}

func assetID(r *http.Request) (uuid.UUID, error) { return pathID(r, "assetId", errAssetNotFound) }

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	qs := r.URL.Query()
	res, err := h.svc.ListAssets(r.Context(), id, AssetFilter{Kind: qs.Get("kind"), Tag: qs.Get("tag"), Search: qs.Get("q"), Status: qs.Get("status")})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in AssetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateAsset(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetAsset(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in AssetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.UpdateAsset(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteAsset(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseStep reads "60", "60s", "5m", "1h" or "1d".
func parseStep(v string) (time.Duration, bool) {
	if v == "" {
		return 0, true
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second, true
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		return time.Duration(n) * 24 * time.Hour, err == nil && n > 0
	}
	d, err := time.ParseDuration(v)
	return d, err == nil && d > 0
}

func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	qs := r.URL.Query()
	var mq MetricsQuery
	var fields []apperr.FieldError
	for name, dst := range map[string]*time.Time{"from": &mq.From, "to": &mq.To} {
		if v := qs.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				fields = append(fields, apperr.FieldError{Field: name, Rule: "datetime"})
			}
			*dst = t
		}
	}
	step, ok := parseStep(qs.Get("step"))
	if !ok {
		fields = append(fields, apperr.FieldError{Field: "step", Rule: "duration"})
	}
	mq.Step = step
	if len(fields) > 0 {
		httpx.Error(w, r, apperr.Validation(fields))
		return
	}
	res, err := h.svc.Metrics(r.Context(), id, mq)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) agentToken(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.IssueAgentToken(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) certificate(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetCertificate(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"certificate": res})
}

func (h *Handler) checkCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := assetID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CheckCertificate(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// parseWithin reads "30d", "72h" or a number of days.
func parseWithin(v string) (time.Duration, bool) {
	if v == "" {
		return 0, true
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 && n <= 3650 {
		return time.Duration(n) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(v)
	return d, err == nil && d > 0
}

func (h *Handler) certificates(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	within, ok := parseWithin(r.URL.Query().Get("expiring_within"))
	if !ok {
		httpx.Error(w, r, apperr.Validation([]apperr.FieldError{{Field: "expiring_within", Rule: "duration"}}))
		return
	}
	res, err := h.svc.ListCertificates(r.Context(), id, within)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		a, err := h.svc.AuthenticateAgent(r.Context(), token)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), assetKey, a)))
	})
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var hb Heartbeat
	if err := httpx.Decode(w, r, &hb); err != nil {
		httpx.Error(w, r, err)
		return
	}
	a, _ := r.Context().Value(assetKey).(store.InfraAsset)
	res, err := h.svc.RecordHeartbeat(r.Context(), a, hb)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
