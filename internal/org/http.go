package org

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/pagination"
)

// Handler exposes organizations, members, invitations and teams over HTTP.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the routes under /api/v1 (every route requires authentication).
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(authn.RequireAuth)

		r.Get("/orgs", h.list)
		r.Post("/orgs", h.create)
		r.Route("/orgs/{orgId}", func(r chi.Router) {
			r.Get("/", h.get)
			r.Patch("/", h.update)
			r.Delete("/", h.delete)
			r.Post("/transfer-ownership", h.transfer)
			r.Get("/permissions", h.permissions)
			r.Get("/members", h.listMembers)
			r.Patch("/members/{userId}", h.updateMember)
			r.Delete("/members/{userId}", h.removeMember)
			r.Get("/invitations", h.listInvitations)
			r.Post("/invitations", h.invite)
			r.Get("/teams", h.listTeams)
			r.Post("/teams", h.createTeam)
		})

		r.Delete("/invitations/{invitationId}", h.revokeInvitation)
		r.Post("/invitations/preview", h.previewInvitation)
		r.Post("/invitations/accept", h.acceptInvitation)

		r.Route("/teams/{teamId}", func(r chi.Router) {
			r.Get("/", h.getTeam)
			r.Patch("/", h.updateTeam)
			r.Delete("/", h.deleteTeam)
			r.Put("/members/{userId}", h.addTeamMember)
			r.Delete("/members/{userId}", h.removeTeamMember)
		})
	})
}

// pathID parses a UUID path parameter; malformed IDs are reported as the resource's 404.
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

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListMine(r.Context(), page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	o, err := h.svc.Create(r.Context(), in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(o.Version))
	httpx.JSON(w, http.StatusCreated, o)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	o, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(o.Version))
	httpx.JSON(w, http.StatusOK, o)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in UpdateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	o, err := h.svc.Update(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(o.Version))
	httpx.JSON(w, http.StatusOK, o)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err == nil {
		err = h.svc.Delete(r.Context(), id, r.URL.Query().Get("confirm"))
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type transferInput struct {
	UserID uuid.UUID `json:"user_id" validate:"required"`
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in transferInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.TransferOwnership(r.Context(), id, in.UserID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) permissions(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, err := h.svc.Permissions(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	q := r.URL.Query()
	res, err := h.svc.ListMembers(r.Context(), id, MemberFilter{Role: q.Get("role"), Search: q.Get("q")}, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type roleInput struct {
	Role string `json:"role" validate:"required,oneof=owner admin developer viewer"`
}

func memberID(r *http.Request) (uuid.UUID, error) { return pathID(r, "userId", errMemberNotFound) }

func (h *Handler) updateMember(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	uid, err := memberID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in roleInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	m, err := h.svc.UpdateMemberRole(r.Context(), id, uid, in.Role)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	uid, err := memberID(r)
	if err == nil {
		err = h.svc.RemoveMember(r.Context(), id, uid)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListInvitations(r.Context(), id, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in InviteInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	inv, err := h.svc.Invite(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, inv)
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "invitationId", errInvitationNotFound)
	if err == nil {
		err = h.svc.RevokeInvitation(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tokenInput struct {
	Token string `json:"token" validate:"required,max=128"`
}

func (h *Handler) previewInvitation(w http.ResponseWriter, r *http.Request) {
	var in tokenInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	p, err := h.svc.PreviewInvitation(r.Context(), in.Token)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var in tokenInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	o, err := h.svc.AcceptInvitation(r.Context(), in.Token)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	page, err := pagination.Parse(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.svc.ListTeams(r.Context(), id, page)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	id, err := orgID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in TeamInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, err := h.svc.CreateTeam(r.Context(), id, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(t.Version))
	httpx.JSON(w, http.StatusCreated, t)
}

func teamID(r *http.Request) (uuid.UUID, error) { return pathID(r, "teamId", errTeamNotFound) }

// TeamDetail is a team with its members.
type TeamDetail struct {
	Team
	Members []TeamMember `json:"members"`
}

func (h *Handler) getTeam(w http.ResponseWriter, r *http.Request) {
	id, err := teamID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, members, err := h.svc.GetTeam(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(t.Version))
	httpx.JSON(w, http.StatusOK, TeamDetail{Team: t, Members: members})
}

func (h *Handler) updateTeam(w http.ResponseWriter, r *http.Request) {
	id, err := teamID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	version, err := httpx.ParseIfMatch(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var in TeamInput
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, err)
		return
	}
	t, err := h.svc.UpdateTeam(r.Context(), id, version, in)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("ETag", httpx.ETag(t.Version))
	httpx.JSON(w, http.StatusOK, t)
}

func (h *Handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	id, err := teamID(r)
	if err == nil {
		err = h.svc.DeleteTeam(r.Context(), id)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addTeamMember(w http.ResponseWriter, r *http.Request) {
	id, err := teamID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	uid, err := memberID(r)
	if err == nil {
		err = h.svc.AddTeamMember(r.Context(), id, uid)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	id, err := teamID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	uid, err := memberID(r)
	if err == nil {
		err = h.svc.RemoveTeamMember(r.Context(), id, uid)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
