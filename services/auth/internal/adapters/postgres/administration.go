package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) Provision(ctx context.Context, u domain.User, operator string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(180018)`); err != nil {
		return err
	}
	if u.ID > 0 {
		old, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1 FOR UPDATE OF u`, u.ID))
		if e != nil {
			return e
		}
		if old.CustomerID != u.CustomerID || old.CourierID != 0 && old.CourierID != u.CourierID {
			return domain.ErrConflict
		}
		if slices.Contains(old.Roles, "admin") && !slices.Contains(u.Roles, "admin") {
			var n int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_users WHERE status='ACTIVE' AND 'admin'=ANY(roles)`).Scan(&n); err != nil {
				return err
			}
			if n <= 1 {
				return domain.ErrConflict
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE auth_users SET email_verified_at=CASE WHEN email=$2 THEN email_verified_at ELSE NULL END,email=$2,password_hash=$3,status='ACTIVE',roles=$4,permissions=$5,auth_version=auth_version+1,updated_at=clock_timestamp() WHERE id=$1`, u.ID, u.Email, u.PasswordHash, u.Roles, u.Permissions); err != nil {
			return conflict(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1`, u.ID); err != nil {
			return err
		}
		if err = queueDeviceRevocation(ctx, tx, u.ID); err != nil {
			return err
		}
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO auth_users(subject,email,password_hash,status,customer_id,roles,permissions) VALUES($1,$2,$3,'ACTIVE',nullif($4,0),$5,$6) RETURNING id`, u.Subject, u.Email, u.PasswordHash, u.CustomerID, u.Roles, u.Permissions).Scan(&u.ID)
		if err != nil {
			return conflict(err)
		}
	}
	if err = bindings(ctx, tx, u.ID, u); err != nil {
		return err
	}
	if err = audit(ctx, tx, operator, "managed_account_provisioned", u.ID); err != nil {
		return err
	}
	if err = advanceAllocators(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Disable(ctx context.Context, id int64, operator string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(180018)`); err != nil {
		return err
	}
	u, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1 FOR UPDATE OF u`, id))
	if err != nil {
		return err
	}
	if u.Status == domain.Active && slices.Contains(u.Roles, "admin") {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_users WHERE status='ACTIVE' AND 'admin'=ANY(roles)`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return domain.ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET status='DISABLED',auth_version=auth_version+1,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err = queueDeviceRevocation(ctx, tx, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, operator, "account_disabled", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Import(ctx context.Context, issuer string, users []domain.User, operator string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(180018)`); err != nil {
		return err
	}
	for _, u := range users {
		var existing int64
		err = tx.QueryRow(ctx, `SELECT user_id FROM auth_legacy_identities WHERE issuer=$1 AND subject=$2`, issuer, u.Subject).Scan(&existing)
		if err == nil {
			if existing != u.ID {
				return domain.ErrConflict
			}
			old, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1`, existing))
			if e != nil {
				return e
			}
			if old.Subject != u.Subject || old.CustomerID != u.CustomerID || old.CourierID != u.CourierID {
				return domain.ErrConflict
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO auth_users(id,subject,status,customer_id,roles,permissions) VALUES($1,$2,'DISABLED',nullif($3,0),$4,$5)`, u.ID, u.Subject, u.CustomerID, u.Roles, u.Permissions)
		if err != nil {
			return conflict(err)
		}
		if err = bindings(ctx, tx, u.ID, u); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO auth_legacy_identities(issuer,subject,user_id) VALUES($1,$2,$3)`, issuer, u.Subject, u.ID); err != nil {
			return err
		}
		if err = audit(ctx, tx, operator, "legacy_identity_imported", u.ID); err != nil {
			return err
		}
	}
	// Never move allocators backwards, including after an import retry.
	if err = advanceAllocators(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func advanceAllocators(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT setval(pg_get_serial_sequence('auth_users','id'),greatest((SELECT last_value FROM auth_users_id_seq),(SELECT coalesce(max(id),1000000000000) FROM auth_users)),true); SELECT setval('auth_customer_ids',greatest((SELECT last_value FROM auth_customer_ids),(SELECT coalesce(max(customer_id),1000000000000) FROM auth_users)),true)`)
	return err
}
