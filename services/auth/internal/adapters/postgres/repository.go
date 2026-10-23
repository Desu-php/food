package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool} }

const columns = `u.id,u.subject,coalesce(u.email,''),coalesce(u.password_hash,''),u.status,coalesce(u.customer_id,0),coalesce((SELECT courier_id FROM auth_courier_bindings WHERE user_id=u.id),0),u.auth_version,u.roles,u.permissions,ARRAY(SELECT restaurant_id FROM auth_restaurant_memberships WHERE user_id=u.id ORDER BY restaurant_id),u.email_verified_at,u.created_at,u.updated_at`

func scan(row pgx.Row) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.Subject, &u.Email, &u.PasswordHash, &u.Status, &u.CustomerID, &u.CourierID, &u.Version, &u.Roles, &u.Permissions, &u.Restaurants, &u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrCredentials
	}
	return u, err
}
func (r *Repository) Ready(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `SELECT u.id,s.id,t.token_hash,l.key_hash,a.action FROM auth_users u LEFT JOIN auth_sessions s ON false LEFT JOIN auth_refresh_tokens t ON false LEFT JOIN auth_rate_limits l ON false LEFT JOIN auth_audit a ON false LIMIT 0; SELECT user_id,lease_token FROM auth_device_revocations LIMIT 0; SELECT user_id FROM auth_restaurant_memberships LIMIT 0; SELECT user_id FROM auth_courier_bindings LIMIT 0; SELECT issuer FROM auth_legacy_identities LIMIT 0`)
	return err
}
func (r *Repository) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	var attempts int
	err := r.pool.QueryRow(ctx, `INSERT INTO auth_rate_limits(key_hash,attempts,reset_at) VALUES($1,1,clock_timestamp()+$2*interval '1 second') ON CONFLICT(key_hash) DO UPDATE SET attempts=CASE WHEN auth_rate_limits.reset_at<=clock_timestamp() THEN 1 ELSE least(auth_rate_limits.attempts+1,$3+1) END,reset_at=CASE WHEN auth_rate_limits.reset_at<=clock_timestamp() THEN clock_timestamp()+$2*interval '1 second' ELSE auth_rate_limits.reset_at END RETURNING attempts`, key, window.Seconds(), limit).Scan(&attempts)
	return attempts <= limit, err
}
func (r *Repository) Register(ctx context.Context, u domain.User) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(180018)`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_users(subject,email,password_hash,status,customer_id,roles) VALUES($1,$2,$3,$4,nextval('auth_customer_ids'),ARRAY['customer']) ON CONFLICT(email) DO NOTHING`, u.Subject, u.Email, u.PasswordHash, u.Status)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) ByEmail(ctx context.Context, email string) (domain.User, error) {
	return scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.email=$1`, email))
}
func (r *Repository) StartSession(ctx context.Context, expected domain.User, s domain.Session, hash string) (domain.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, err
	}
	defer tx.Rollback(ctx)
	u, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1 FOR UPDATE OF u`, expected.ID))
	if err != nil {
		return u, err
	}
	if u.Status != domain.Active || u.PasswordHash != expected.PasswordHash || u.Version != expected.Version {
		return u, domain.ErrCredentials
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id IN (SELECT id FROM auth_sessions WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC OFFSET 9)`, u.ID); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,auth_version,created_at,expires_at) VALUES($1,$2,$3,$4,$5)`, s.ID, u.ID, u.Version, s.CreatedAt, s.ExpiresAt); err != nil {
		return u, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_refresh_tokens(token_hash,session_id) VALUES($1,$2)`, hash, s.ID); err != nil {
		return u, err
	}
	return u, tx.Commit(ctx)
}
func (r *Repository) Rotate(ctx context.Context, old, next string) (domain.User, domain.Session, error) {
	var id int64
	var sid string
	err := r.pool.QueryRow(ctx, `SELECT s.user_id,s.id FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE t.token_hash=$1`, old).Scan(&id, &sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.Session{}, domain.ErrCredentials
	}
	if err != nil {
		return domain.User{}, domain.Session{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, domain.Session{}, err
	}
	defer tx.Rollback(ctx)
	u, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1 FOR UPDATE OF u`, id))
	if err != nil {
		return u, domain.Session{}, err
	}
	var s domain.Session
	var revoked, consumed *time.Time
	var version int64
	var live bool
	err = tx.QueryRow(ctx, `SELECT id,created_at,expires_at,revoked_at,auth_version,expires_at>clock_timestamp() FROM auth_sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&s.ID, &s.CreatedAt, &s.ExpiresAt, &revoked, &version, &live)
	if err != nil {
		return u, s, err
	}
	err = tx.QueryRow(ctx, `SELECT consumed_at FROM auth_refresh_tokens WHERE token_hash=$1 FOR UPDATE`, old).Scan(&consumed)
	if err != nil {
		return u, s, err
	}
	if consumed != nil {
		if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE id=$1`, sid); err != nil {
			return u, s, err
		}
		if err = audit(ctx, tx, "session:"+sid, "refresh_reuse", u.ID); err != nil {
			return u, s, err
		}
		if err = tx.Commit(ctx); err != nil {
			return u, s, err
		}
		return u, s, domain.ErrCredentials
	}
	if !live || revoked != nil || u.Status != domain.Active || u.Version != version {
		return u, s, domain.ErrCredentials
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_refresh_tokens SET consumed_at=clock_timestamp() WHERE token_hash=$1`, old); err != nil {
		return u, s, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth_refresh_tokens(token_hash,session_id) VALUES($1,$2)`, next, sid); err != nil {
		return u, s, err
	}
	return u, s, tx.Commit(ctx)
}
func (r *Repository) Authenticate(ctx context.Context, c domain.Claims) (domain.User, error) {
	return scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u JOIN auth_sessions s ON s.user_id=u.id WHERE u.subject=$1 AND s.id=$2 AND u.auth_version=$3 AND s.auth_version=u.auth_version AND u.status='ACTIVE' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`, c.Subject, c.SessionID, c.Version))
}
func current(ctx context.Context, tx pgx.Tx, c domain.Claims) (domain.User, error) {
	u, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.subject=$1 FOR UPDATE OF u`, c.Subject))
	if err != nil {
		return u, err
	}
	var live bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND auth_version=$3 AND revoked_at IS NULL AND expires_at>clock_timestamp())`, c.SessionID, u.ID, u.Version).Scan(&live)
	if err != nil {
		return u, err
	}
	if !live || u.Status != domain.Active || u.Version != c.Version || !c.ExpiresAt.After(time.Now()) {
		return u, domain.ErrCredentials
	}
	return u, nil
}
func (r *Repository) Logout(ctx context.Context, c domain.Claims, all bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := current(ctx, tx, c)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1 AND ($2 OR id=$3)`, u.ID, all, c.SessionID); err != nil {
		return err
	}
	if err = audit(ctx, tx, strconv.FormatInt(u.ID, 10), "logout", u.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) LogoutToken(ctx context.Context, hash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	var sid string
	err = tx.QueryRow(ctx, `SELECT s.user_id,s.id FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id=t.session_id WHERE t.token_hash=$1`, hash).Scan(&id, &sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM auth_users WHERE id=$1 FOR UPDATE`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE id=$1`, sid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) DeviceAuthorized(ctx context.Context, id int64, kind string, owner int64) (bool, error) {
	var allowed bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users u WHERE u.id=$1 AND u.status='ACTIVE' AND (($2='CUSTOMER' AND 'customer'=ANY(u.roles) AND u.customer_id=$3) OR ($2='COURIER' AND 'courier'=ANY(u.roles) AND EXISTS(SELECT 1 FROM auth_courier_bindings c WHERE c.user_id=u.id AND c.courier_id=$3)) OR ($2='RESTAURANT' AND 'restaurant_staff'=ANY(u.roles) AND EXISTS(SELECT 1 FROM auth_restaurant_memberships m WHERE m.user_id=u.id AND m.restaurant_id=$3))))`, id, kind, owner).Scan(&allowed)
	return allowed, err
}
func (r *Repository) ChangePassword(ctx context.Context, c domain.Claims, old, next string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := current(ctx, tx, c)
	if err != nil {
		return err
	}
	if u.PasswordHash != old {
		return domain.ErrCredentials
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET password_hash=$2,auth_version=auth_version+1,updated_at=clock_timestamp() WHERE id=$1`, u.ID, next); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1`, u.ID); err != nil {
		return err
	}
	if err = audit(ctx, tx, strconv.FormatInt(u.ID, 10), "password_changed", u.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) Sessions(ctx context.Context, c domain.Claims) ([]domain.Session, error) {
	u, err := r.Authenticate(ctx, c)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id,created_at,expires_at FROM auth_sessions WHERE user_id=$1 AND auth_version=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() ORDER BY created_at DESC LIMIT 10`, u.ID, u.Version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Session{}
	for rows.Next() {
		var s domain.Session
		if err = rows.Scan(&s.ID, &s.CreatedAt, &s.ExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
func (r *Repository) RevokeSession(ctx context.Context, c domain.Claims, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := current(ctx, tx, c)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1 AND id=$2`, u.ID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) UpdateBindings(ctx context.Context, c domain.Claims, id int64, next domain.User) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(180018)`); err != nil {
		return err
	}
	actor, err := current(ctx, tx, c)
	if err != nil {
		return err
	}
	if !slices.Contains(actor.Roles, "admin") {
		return domain.ErrForbidden
	}
	target, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM auth_users u WHERE u.id=$1 FOR UPDATE OF u`, id))
	if err != nil {
		return err
	}
	if target.CustomerID != next.CustomerID || target.CourierID != 0 && target.CourierID != next.CourierID {
		return domain.ErrConflict
	}
	if slices.Contains(target.Roles, "admin") && !slices.Contains(next.Roles, "admin") {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM auth_users WHERE status='ACTIVE' AND 'admin'=ANY(roles)`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return domain.ErrConflict
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_users SET roles=$2,permissions=$3,auth_version=auth_version+1,updated_at=clock_timestamp() WHERE id=$1`, id, next.Roles, next.Permissions); err != nil {
		return err
	}
	if err = bindings(ctx, tx, id, next); err != nil {
		return err
	}
	if err = queueDeviceRevocation(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE user_id=$1`, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, strconv.FormatInt(actor.ID, 10), "bindings_changed", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func bindings(ctx context.Context, tx pgx.Tx, id int64, u domain.User) error {
	if _, err := tx.Exec(ctx, `DELETE FROM auth_restaurant_memberships WHERE user_id=$1`, id); err != nil {
		return err
	}
	for _, rid := range u.Restaurants {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_restaurant_memberships(user_id,restaurant_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, rid); err != nil {
			return err
		}
	}
	if u.CourierID > 0 {
		_, err := tx.Exec(ctx, `INSERT INTO auth_courier_bindings(user_id,courier_id) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET courier_id=EXCLUDED.courier_id`, id, u.CourierID)
		if err != nil {
			return conflict(err)
		}
	}
	return nil
}
func audit(ctx context.Context, tx pgx.Tx, actor, action string, id int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO auth_audit(actor,action,user_id) VALUES($1,$2,$3)`, actor, action, id)
	return err
}
func queueDeviceRevocation(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO auth_device_revocations(user_id) VALUES($1) ON CONFLICT(user_id) DO UPDATE SET not_after=clock_timestamp(),available_at=clock_timestamp(),lease_token=NULL,lease_until=NULL,attempts=0`, id)
	return err
}
func (r *Repository) Prune(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM auth_sessions WHERE expires_at<clock_timestamp()-interval '7 days' ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT 100`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_refresh_tokens WHERE session_id=ANY($1)`, ids); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_sessions WHERE id=ANY($1)`, ids); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM auth_rate_limits WHERE key_hash IN (SELECT key_hash FROM auth_rate_limits WHERE reset_at<clock_timestamp()-interval '1 day' ORDER BY reset_at FOR UPDATE SKIP LOCKED LIMIT 1000)`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func conflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return domain.ErrConflict
	}
	return fmt.Errorf("store managed identity: %w", err)
}
