package alert

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/store"
)

// Handler exposes alert rules, alerts and silences.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)
		r.Get("/orgs/{orgId}/alert-rules", h.listRules)
		r.Post("/orgs/{orgId}/alert-rules", h.createRule)
		r.Get("/alert-rules/{ruleId}", h.getRule)
		r.Patch("/alert-rules/{ruleId}", h.updateRule)
		r.Delete("/alert-rules/{ruleId}", h.deleteRule)
		r.Get("/orgs/{orgId}/alerts", h.listAlerts)
		r.Get("/alerts/{alertId}", h.getAlert)
		r.Post("/alerts/{alertId}/acknowledge", h.acknowledge)
		r.Get("/orgs/{orgId}/silences", h.listSilences)
		r.Post("/orgs/{orgId}/silences", h.createSilence)
		r.Delete("/silences/{silenceId}", h.expireSilence)
	})
}

func pathID(r *http.Request, name string, nf func() *apperr.Error) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, nf()
	}
	return id, nil
}

func orgID(r *http.Request) (uuid.UUID, error) {
	return pathID(r, "orgId", func() *apperr.Error {
		return apperr.New(apperr.CodeOrgNotFound, http.StatusNotFound, "organization not found")
	})
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListRules(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in RuleInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateRule(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) getRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "ruleId", errRuleNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetRule(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "ruleId", errRuleNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in RuleInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.UpdateRule(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "ruleId", errRuleNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.DeleteRule(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	qs := r.URL.Query()
	var f AlertFilter
	var fields []apperr.FieldError
	switch v := store.AlertStatus(qs.Get("status")); v {
	case "":
	case store.AlertStatusFiring, store.AlertStatusResolved:
		f.Status = &v
	default:
		fields = append(fields, apperr.FieldError{Field: "status", Rule: "oneof", Param: "firing resolved"})
	}
	switch v := store.AlertSeverity(qs.Get("severity")); v {
	case "":
	case store.AlertSeverityInfo, store.AlertSeverityWarning, store.AlertSeverityCritical:
		f.Severity = &v
	default:
		fields = append(fields, apperr.FieldError{Field: "severity", Rule: "oneof", Param: "info warning critical"})
	}
	if v := qs.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			fields = append(fields, apperr.FieldError{Field: "before", Rule: "datetime"})
		}
		f.Before = &t
	}
	if len(fields) > 0 {
		httpx.Error(w, r, apperr.Validation(fields))
		return
	}
	res, err := h.svc.ListAlerts(r.Context(), id, f)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) getAlert(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "alertId", errAlertNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.GetAlert(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "alertId", errAlertNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.Acknowledge(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) listSilences(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListSilences(r.Context(), id, r.URL.Query().Get("include_expired") == "true")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
}

func (h *Handler) createSilence(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in SilenceInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.CreateSilence(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (h *Handler) expireSilence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "silenceId", errSilenceNotFound)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.ExpireSilence(r.Context(), id); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
