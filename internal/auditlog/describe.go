package auditlog

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/i18n"
)

// row is an audit entry with the names joined for it (organization or account view).
type row struct {
	ActorType         string
	ActorUserID       *uuid.UUID
	Action            string
	ResourceType      string
	ResourceID        *string
	Before            []byte
	After             []byte
	Metadata          []byte
	ActorName         *string
	ActorEmail        string
	ProjectName       *string
	ResourceUserName  *string
	ResourceUserEmail string
}

// names resolves ids that entries mention only by id (teams, users in team membership,
// assets whose agent token changed).
type names struct {
	teams  map[uuid.UUID]string
	users  map[uuid.UUID]string
	assets map[uuid.UUID]string
}

func object(raw []byte) map[string]any {
	var m map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// text returns m[key] as display text: strings as they are, whole numbers without decimals.
func text(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var providers = map[string]string{"github": "GitHub", "gitlab": "GitLab", "google": "Google", "keycloak": "Keycloak", "gitea": "Gitea"}

func providerName(p string) string {
	if n, ok := providers[strings.ToLower(p)]; ok {
		return n
	}
	return p
}

// shortRef turns refs/heads/main into main and refs/tags/v1 into v1.
func shortRef(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/pull/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}

func (n names) team(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return n.teams[u]
	}
	return ""
}

func (n names) user(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return n.users[u]
	}
	return ""
}

// actor names who did it: a user (or their API token), a runner, or OpsHub itself.
func actor(b *i18n.Bundle, loc string, r row) string {
	switch r.ActorType {
	case "system":
		return b.T(loc, "audit.actor.system", nil)
	case "runner":
		return b.T(loc, "audit.actor.runner", nil)
	}
	name := first(deref(r.ActorName), r.ActorEmail)
	if name == "" {
		return b.T(loc, "audit.actor.deleted", nil)
	}
	if r.ActorType == "api_token" {
		return b.T(loc, "audit.actor.api_token", map[string]any{"Name": name})
	}
	return name
}

// Describe renders an entry as one sentence in loc, e.g. "Sok Chan revoked the ingest token
// Shop API for shop/api". Unknown actions fall back to "<actor>: <action> (<resource>)".
func describe(b *i18n.Bundle, loc string, r row, n names) string {
	after, before, meta := object(r.After), object(r.Before), object(r.Metadata)
	resID := deref(r.ResourceID)
	unnamed := b.T(loc, "audit.unnamed", nil)
	orUnnamed := func(s string) string { return first(s, unnamed) }

	target := ""
	switch r.ResourceType {
	case "user", "member":
		target = first(deref(r.ResourceUserName), r.ResourceUserEmail, text(meta, "email"))
	case "team":
		target = first(n.team(resID), text(after, "name"), text(before, "name"))
	case "asset":
		if id, err := uuid.Parse(resID); err == nil {
			target = n.assets[id]
		}
	}
	for _, m := range []map[string]any{after, before, meta} {
		target = first(target, text(m, "name"), text(m, "full_name"), text(m, "email"), text(m, "slug"))
	}
	role := func(m map[string]any) string {
		if v := text(m, "role"); v != "" {
			return b.T(loc, "audit.role."+v, nil)
		}
		return unnamed
	}

	data := map[string]any{
		"Actor":    actor(b, loc, r),
		"Target":   orUnnamed(target),
		"Project":  orUnnamed(first(deref(r.ProjectName), text(meta, "project"))),
		"Number":   first(text(after, "number"), text(before, "number"), text(after, "run_number")),
		"Version":  first(text(after, "version"), text(meta, "version")),
		"Role":     role(after),
		"OldRole":  role(before),
		"Action":   r.Action,
		"Resource": strings.TrimSpace(r.ResourceType + " " + resID),
	}
	key := "audit." + r.Action
	switch r.Action {
	case "auth.login_failed":
		key += "." + text(meta, "reason")
	case "auth.locked":
		data["Minutes"] = text(meta, "minutes")
	case "identity.link", "identity.unlink":
		data["Provider"] = providerName(first(text(after, "provider"), text(before, "provider")))
	case "org.update":
		data["OldName"] = orUnnamed(text(before, "name"))
	case "org.transfer":
		data["NewOwner"] = orUnnamed(text(after, "new_owner"))
	case "member.join", "member.leave":
		// The actor is the member.
	case "team.add_member", "team.remove_member":
		data["Member"] = orUnnamed(first(text(after, "email"), n.user(first(text(after, "user_id"), text(before, "user_id")))))
		data["Target"] = orUnnamed(first(n.team(resID), text(after, "team")))
	case "pipeline.trigger":
		data["Ref"] = orUnnamed(shortRef(text(after, "ref")))
	case "run.rerun":
		data["From"] = text(after, "from_number")
	case "approval.decide":
		key += "." + text(after, "decision")
		data["Job"] = orUnnamed(text(after, "job"))
	case "deployment.create", "deployment.rollback":
		data["Environment"] = orUnnamed(text(after, "environment"))
	case "alert.ack":
		data["Rule"] = orUnnamed(text(meta, "rule"))
		data["Subject"] = orUnnamed(text(meta, "subject"))
	case "log_token.create", "log_token.revoke":
		data["Service"] = orUnnamed(first(text(after, "service"), text(before, "service")))
	}
	if s, ok := b.Lookup(loc, key, data); ok {
		return s
	}
	if base := "audit." + r.Action; base != key {
		if s, ok := b.Lookup(loc, base, data); ok {
			return s
		}
	}
	return b.T(loc, "audit.unknown", data)
}
