package postgres

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/Desu-php/food/services/notifications/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PushStore struct{ pool *pgxpool.Pool }

func NewPushStore(pool *pgxpool.Pool) *PushStore { return &PushStore{pool} }

const deviceColumns = `id,owner_type,owner_id,registered_by,platform,is_active,last_seen_at,created_at,updated_at`

func scanDevice(row pgx.Row) (domain.DeviceRegistration, error) {
	var d domain.DeviceRegistration
	err := row.Scan(&d.ID, &d.OwnerType, &d.OwnerID, &d.RegisteredBy, &d.Platform, &d.IsActive, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}
func (r *PushStore) Register(ctx context.Context, d domain.DeviceRegistration) (domain.DeviceRegistration, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer rollback(tx)
	hash := sha256.Sum256([]byte(d.InstallationID))
	// Serializes both first registration and ownership transfer for this installation.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(encode($1::bytea,'hex'),0))`, hash[:]); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, `UPDATE device_registrations SET is_active=false,installation_id='revoked-device:'||id,updated_at=clock_timestamp() WHERE target_hash=$1 AND is_active AND (registered_by<>$2 OR owner_type<>$3 OR owner_id<>$4)`, hash[:], d.RegisteredBy, d.OwnerType, d.OwnerID); err != nil {
		return d, err
	}
	result, err := scanDevice(tx.QueryRow(ctx, `INSERT INTO device_registrations(owner_type,owner_id,registered_by,platform,installation_id,target_hash) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(target_hash) WHERE is_active DO UPDATE SET last_seen_at=clock_timestamp(),updated_at=clock_timestamp(),platform=EXCLUDED.platform RETURNING `+deviceColumns, d.OwnerType, d.OwnerID, d.RegisteredBy, d.Platform, d.InstallationID, hash[:]))
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (r *PushStore) Devices(ctx context.Context, user int64) ([]domain.DeviceRegistration, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+deviceColumns+` FROM device_registrations WHERE registered_by=$1 ORDER BY id DESC LIMIT 200`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.DeviceRegistration{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func (r *PushStore) Deactivate(ctx context.Context, user, id int64) error {
	tag, err := r.pool.Exec(ctx, `UPDATE device_registrations SET is_active=false,installation_id='revoked-device:'||id,updated_at=clock_timestamp() WHERE id=$1 AND registered_by=$2`, id, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
func (r *PushStore) PruneDevices(ctx context.Context, stale time.Duration) error {
	_, err := r.pool.Exec(ctx, `WITH old AS(SELECT id FROM device_registrations WHERE is_active AND last_seen_at<clock_timestamp()-$1*interval '1 millisecond' ORDER BY last_seen_at,id FOR UPDATE SKIP LOCKED LIMIT 1000) UPDATE device_registrations d SET is_active=false,installation_id='revoked-device:'||d.id,updated_at=clock_timestamp() FROM old WHERE d.id=old.id`, stale.Milliseconds())
	return err
}
