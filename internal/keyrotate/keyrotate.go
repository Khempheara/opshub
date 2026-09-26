// Package keyrotate re-encrypts every value stored under the master key ring with the
// active key (`opshub-api keys rotate`), so an old master key can then be removed from
// OPSHUB_MASTER_KEYS. Secret values are envelope-encrypted: only their data keys are
// re-wrapped. Rows already on the active key are skipped, so the command is idempotent and
// can be resumed after an interruption.
package keyrotate

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/crypto"
)

// column is one encrypted column: how to list its rows, derive each row's AAD, and write
// the new value back only if the row still holds the old one.
type column struct {
	name string
	// list pages by (id, extra) > ($1, $3), LIMIT $2, returning id, extra, value. extra is
	// the second key column (a secret's version) or 0.
	list string
	// update sets $3 where (id, extra) = ($1, $2) and the value is still $4 (the old one);
	// $5 is the new key id when kekID is set.
	update string
	aad    func(id uuid.UUID, extra int32) []byte
	// kekID also records the new key id (secret versions).
	kekID bool
}

var columns = []column{
	{
		name:   "secret_versions.dek_enc",
		list:   `SELECT secret_id, version, dek_enc FROM secret_versions WHERE dek_enc IS NOT NULL AND (secret_id, version) > ($1, $3) ORDER BY secret_id, version LIMIT $2`,
		update: `UPDATE secret_versions SET dek_enc = $3, kek_id = $5 WHERE secret_id = $1 AND version = $2 AND dek_enc = $4`,
		aad:    func(id uuid.UUID, v int32) []byte { return fmt.Appendf(nil, "secret:%s:%d", id, v) },
		kekID:  true,
	},
	{
		name:   "users.totp_secret_enc",
		list:   `SELECT id, 0, totp_secret_enc FROM users WHERE totp_secret_enc IS NOT NULL AND (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE users SET totp_secret_enc = $3 WHERE id = $1 AND $2 = 0 AND totp_secret_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return []byte("totp:" + id.String()) },
	},
	{
		name:   "users.totp_pending_enc",
		list:   `SELECT id, 0, totp_pending_enc FROM users WHERE totp_pending_enc IS NOT NULL AND (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE users SET totp_pending_enc = $3 WHERE id = $1 AND $2 = 0 AND totp_pending_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return []byte("totp:" + id.String()) },
	},
	{
		name:   "repositories.access_token_enc",
		list:   `SELECT id, 0, access_token_enc FROM repositories WHERE (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE repositories SET access_token_enc = $3 WHERE id = $1 AND $2 = 0 AND access_token_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return id[:] },
	},
	{
		name:   "repositories.webhook_secret_enc",
		list:   `SELECT id, 0, webhook_secret_enc FROM repositories WHERE (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE repositories SET webhook_secret_enc = $3 WHERE id = $1 AND $2 = 0 AND webhook_secret_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return id[:] },
	},
	{
		name:   "deploy_targets.credentials_enc",
		list:   `SELECT id, 0, credentials_enc FROM deploy_targets WHERE (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE deploy_targets SET credentials_enc = $3 WHERE id = $1 AND $2 = 0 AND credentials_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return id[:] },
	},
	{
		name:   "notification_channels.secrets_enc",
		list:   `SELECT id, 0, secrets_enc FROM notification_channels WHERE (id, 0) > ($1, $3) ORDER BY id LIMIT $2`,
		update: `UPDATE notification_channels SET secrets_enc = $3 WHERE id = $1 AND $2 = 0 AND secrets_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return id[:] },
	},
	{
		name:   "job_tokens.masks_enc",
		list:   `SELECT job_id, 0, masks_enc FROM job_tokens WHERE masks_enc IS NOT NULL AND (job_id, 0) > ($1, $3) ORDER BY job_id LIMIT $2`,
		update: `UPDATE job_tokens SET masks_enc = $3 WHERE job_id = $1 AND $2 = 0 AND masks_enc = $4`,
		aad:    func(id uuid.UUID, _ int32) []byte { return []byte("job-masks:" + id.String()) },
	},
}

// Result counts one column's rows.
type Result struct {
	Column    string `json:"column"`
	Scanned   int    `json:"scanned"`
	Rewrapped int    `json:"rewrapped"`
	// Failed rows couldn't be decrypted with any configured key (their key was removed
	// already, or the row is corrupt). They are left unchanged.
	Failed int `json:"failed"`
}

// ErrUnreadable is returned (with the results) when some rows couldn't be re-encrypted.
var ErrUnreadable = errors.New("some values could not be decrypted with the configured keys")

var batch = 200 // rows per page (tests lower it to exercise paging)

// Rotate re-encrypts every column. It returns ErrUnreadable when rows failed; the old key
// must stay configured until that is resolved.
func Rotate(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing) ([]Result, error) {
	var out []Result
	failed := false
	for _, c := range columns {
		r, err := rotateColumn(ctx, pool, keys, c)
		if err != nil {
			return out, fmt.Errorf("%s: %w", c.name, err)
		}
		out = append(out, r)
		failed = failed || r.Failed > 0
	}
	if failed {
		return out, ErrUnreadable
	}
	return out, nil
}

func rotateColumn(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing, c column) (Result, error) {
	res := Result{Column: c.name}
	type row struct {
		id    uuid.UUID
		extra int32
		value []byte
	}
	var lastID uuid.UUID
	lastExtra := int32(-1)
	for {
		rows, err := pool.Query(ctx, c.list, lastID, batch, lastExtra)
		if err != nil {
			return res, err
		}
		var page []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.extra, &r.value); err != nil {
				rows.Close()
				return res, err
			}
			page = append(page, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, err
		}
		for _, r := range page {
			res.Scanned++
			nv, changed, err := keys.Rewrap(r.value, c.aad(r.id, r.extra))
			if err != nil {
				res.Failed++
				continue
			}
			if !changed {
				continue
			}
			args := []any{r.id, r.extra, nv, r.value}
			if c.kekID {
				args = append(args, keys.ActiveKeyID())
			}
			tag, err := pool.Exec(ctx, c.update, args...)
			if err != nil {
				return res, err
			}
			// 0 rows: the value changed meanwhile (it was just written with the active key).
			res.Rewrapped += int(tag.RowsAffected())
		}
		if len(page) < batch {
			return res, nil
		}
		lastID, lastExtra = page[len(page)-1].id, page[len(page)-1].extra
	}
}
