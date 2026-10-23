package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Revocation struct {
	UserID   int64
	NotAfter time.Time
	Token    string
	Attempts int
}

func (r *Repository) ClaimRevocation(ctx context.Context, token string) (Revocation, bool, error) {
	v := Revocation{Token: token}
	err := r.pool.QueryRow(ctx, `WITH due AS(SELECT user_id FROM auth_device_revocations WHERE available_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY available_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE auth_device_revocations r SET lease_token=$1,lease_until=clock_timestamp()+interval '30 seconds',attempts=attempts+1 FROM due WHERE r.user_id=due.user_id RETURNING r.user_id,r.not_after,r.attempts`, token).Scan(&v.UserID, &v.NotAfter, &v.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}
func (r *Repository) FinishRevocation(ctx context.Context, v Revocation, success bool) error {
	if success {
		_, err := r.pool.Exec(ctx, `DELETE FROM auth_device_revocations WHERE user_id=$1 AND lease_token=$2`, v.UserID, v.Token)
		return err
	}
	delay := min(time.Second*time.Duration(1<<min(v.Attempts, 10)), 15*time.Minute)
	_, err := r.pool.Exec(ctx, `UPDATE auth_device_revocations SET lease_token=NULL,lease_until=NULL,available_at=clock_timestamp()+$3*interval '1 second' WHERE user_id=$1 AND lease_token=$2`, v.UserID, v.Token, delay.Seconds())
	return err
}
