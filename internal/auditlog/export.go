package auditlog

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// utf8BOM starts the file so Excel reads it as UTF-8 (Khmer text).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// exportBatch rows are read per query while streaming.
const exportBatch = 500

// ExportHeader is the CSV's first row (column names stay English for scripts).
var ExportHeader = []string{
	"time", "actor_type", "actor_name", "actor_email", "action", "summary", "resource_type", "resource_id",
	"project_id", "project_name", "ip", "user_agent", "before", "after", "metadata",
}

// safeCell stops spreadsheets from reading a cell as a formula (CSV injection): values
// starting with = + - @ tab or carriage return get a leading apostrophe.
func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func optional(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Export is a prepared CSV export: the caller checked, recorded and named it, and Write
// streams it.
type Export struct {
	// Filename is audit-log-<org slug>-<UTC date>.csv.
	Filename string
	write    func(w io.Writer) error
}

// Write streams the CSV: a UTF-8 byte-order mark (so Excel shows Khmer correctly), the header
// and every matching entry, newest first.
func (e Export) Write(w io.Writer) error { return e.write(w) }

// PrepareExport checks audit.export, validates the filter and records the export (with its
// filter) before anything is sent, so an export is never unaudited.
func (s *Service) PrepareExport(ctx context.Context, orgID uuid.UUID, f Filter, loc string) (Export, error) {
	p, err := f.params()
	if err != nil {
		// Permission errors come first: check before reporting a bad filter.
		if _, aerr := authz.Require(ctx, store.New(s.pool), orgID, authz.AuditExport); aerr != nil {
			return Export{}, aerr
		}
		return Export{}, err
	}
	var slug string
	err = database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		m, err := authz.Require(ctx, q, orgID, authz.AuditExport)
		if err != nil {
			return err
		}
		o, err := q.GetOrganizationForMember(ctx, store.GetOrganizationForMemberParams{ID: orgID, UserID: m.UserID})
		if err != nil {
			return err
		}
		slug = o.Slug
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &orgID, Action: string(authz.AuditExport), ResourceType: "audit_log", ResourceID: orgID.String(),
			Metadata: map[string]any{"filter": filterMeta(f), "locale": loc},
		})
	})
	if err != nil {
		return Export{}, err
	}
	p.OrganizationID = &orgID
	return Export{
		Filename: fmt.Sprintf("audit-log-%s-%s.csv", slug, s.now().UTC().Format("20060102")),
		write:    func(w io.Writer) error { return s.stream(ctx, p, loc, w) },
	}, nil
}

// filterMeta is the filter as recorded with the export (only what was set).
func filterMeta(f Filter) map[string]any {
	m := map[string]any{}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	if len(f.Areas) > 0 {
		m["area"] = f.Areas
	}
	if len(f.Actions) > 0 {
		m["action"] = f.Actions
	}
	set("actor", f.Actor)
	set("project", f.Project)
	set("resource_type", f.ResourceType)
	set("resource_id", f.ResourceID)
	set("from", f.From)
	set("to", f.To)
	return m
}

func (s *Service) stream(ctx context.Context, p store.SearchAuditLogParams, loc string, w io.Writer) error {
	if _, err := w.Write(utf8BOM); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(ExportHeader); err != nil {
		return err
	}
	q := store.New(s.pool)
	p.MaxRows = exportBatch
	for {
		rows, err := q.SearchAuditLog(ctx, p)
		if err != nil {
			return err
		}
		conv := make([]row, len(rows))
		for i, r := range rows {
			conv[i] = searchRow(r)
		}
		n, err := lookupNames(ctx, q, conv)
		if err != nil {
			return err
		}
		for i, r := range rows {
			e := s.entry(loc, r.ID, r.CreatedAt, ipString(r), r.UserAgent, conv[i], n)
			var projectID, projectName string
			if e.Project != nil {
				projectID, projectName = e.Project.ID, optional(e.Project.Name)
			}
			rec := []string{
				e.CreatedAt.UTC().Format(time.RFC3339Nano), e.Actor.Type, optional(e.Actor.Name), optional(e.Actor.Email),
				e.Action, e.Summary, e.ResourceType, optional(e.ResourceID), projectID, projectName,
				optional(e.IP), optional(e.UserAgent), string(e.Before), string(e.After), string(e.Metadata),
			}
			for j := range rec {
				rec[j] = safeCell(rec[j])
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if len(rows) < exportBatch {
			return nil
		}
		last := rows[len(rows)-1]
		p.BeforeCreatedAt, p.BeforeID = &last.CreatedAt, &last.ID
	}
}
