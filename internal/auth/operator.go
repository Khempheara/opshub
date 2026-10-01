package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// ErrNoSuchAccount: VerifyEmailByOperator found no account with that address.
var ErrNoSuchAccount = errors.New("no account with that email address (register first)")

// VerifyEmailByOperator confirms an account's email address without the emailed link. It is
// for the server operator (`opshub-api users verify <email>`), e.g. to let the first admin in
// before email delivery works. The change is audited as done by OpsHub. It reports whether the
// address was confirmed already (then nothing changes).
func VerifyEmailByOperator(ctx context.Context, pool *pgxpool.Pool, email string) (already bool, err error) {
	err = database.InTx(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		u, err := q.GetUserByEmail(ctx, normalizeEmail(email))
		if database.IsNoRows(err) {
			return ErrNoSuchAccount
		}
		if err != nil {
			return err
		}
		if u.EmailVerifiedAt != nil {
			already = true
			return nil
		}
		if err := q.SetUserEmailVerified(ctx, u.ID); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "user.verify_operator", ResourceType: "user", ResourceID: u.ID.String(),
		})
	})
	return already, err
}
