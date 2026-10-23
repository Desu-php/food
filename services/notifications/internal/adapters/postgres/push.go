package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	ids "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *PushStore) Expand(ctx context.Context) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM notifications WHERE status='PENDING' ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO push_deliveries(notification_id,device_id) SELECT n.id,d.id FROM notifications n JOIN device_registrations d ON d.owner_type=n.recipient_type AND d.owner_id=n.recipient_id AND d.is_active WHERE n.id=$1 AND n.expires_at>clock_timestamp() AND n.title<>''`, id)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE notifications SET status=CASE WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1) THEN 'PROCESSING' ELSE 'CANCELLED' END,reason=CASE WHEN expires_at<=clock_timestamp() THEN 'expired' WHEN NOT EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1) THEN 'no_active_devices' ELSE '' END,updated_at=clock_timestamp() WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func (r *PushStore) ClaimPush(ctx context.Context, cfg application.PushConfig) (domain.Delivery, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delivery{}, false, err
	}
	defer rollback(tx)
	// Parent-first lock order serializes siblings, cancellation and completion while
	// SKIP LOCKED permits other notifications to progress on different replicas.
	var notification int64
	err = tx.QueryRow(ctx, `SELECT n.id FROM notifications n WHERE n.status='PROCESSING' AND NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL) AND EXISTS(SELECT FROM push_deliveries d WHERE d.notification_id=n.id AND ((d.status='PENDING' AND d.next_attempt_at<=clock_timestamp()) OR (d.status='PROCESSING' AND d.lease_until<=clock_timestamp()))) ORDER BY n.created_at,n.id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&notification)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delivery{}, false, nil
	}
	if err != nil {
		return domain.Delivery{}, false, err
	}
	var d domain.Delivery
	d.NotificationID = notification
	d.Token = ids.NewID()
	err = tx.QueryRow(ctx, `SELECT id,device_id,attempts FROM push_deliveries WHERE notification_id=$1 AND ((status='PENDING' AND next_attempt_at<=clock_timestamp()) OR(status='PROCESSING' AND lease_until<=clock_timestamp())) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1`, notification).Scan(&d.ID, &d.DeviceID, &d.Attempts)
	if err != nil {
		return d, false, err
	}
	if d.Attempts >= cfg.MaxAttempts {
		if _, err = tx.Exec(ctx, `UPDATE push_deliveries SET status='FAILED',last_code='attempts_exhausted_unknown',claim_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1`, d.ID); err != nil {
			return d, false, err
		}
		if err = finalize(ctx, tx, notification); err != nil {
			return d, false, err
		}
		return d, false, tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `UPDATE push_rate_limit SET next_at=clock_timestamp()+$1*interval '1 millisecond' WHERE id AND next_at<=clock_timestamp()`, cfg.SendInterval.Milliseconds())
	if err != nil {
		return d, false, err
	}
	if tag.RowsAffected() == 0 {
		return d, false, nil
	}
	d.Attempts++
	if _, err = tx.Exec(ctx, `UPDATE push_deliveries SET status='PROCESSING',claim_token=$2,lease_until=clock_timestamp()+$3*interval '1 millisecond',attempts=$4,updated_at=clock_timestamp() WHERE id=$1`, d.ID, d.Token, cfg.Lease.Milliseconds(), d.Attempts); err != nil {
		return d, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO push_attempts(delivery_id,attempt) VALUES($1,$2)`, d.ID, d.Attempts); err != nil {
		return d, false, err
	}
	err = tx.QueryRow(ctx, `SELECT recipient_type,recipient_id,event_id::text,kind,correlation_id,COALESCE(offer_id,0),title,body,data,expires_at,created_at FROM notifications WHERE id=$1`, notification).Scan(&d.Recipient.Type, &d.Recipient.ID, &d.EventID, &d.EventType, &d.CorrelationID, &d.OfferID, &d.Message.Title, &d.Message.Body, &d.Message.Data, &d.Message.ExpiresAt, &d.CreatedAt)
	if err != nil {
		return d, false, err
	}
	d.Message.CollapseKey = fmt.Sprintf("notification-%d", notification)
	return d, true, tx.Commit(ctx)
}
func (r *PushStore) Current(ctx context.Context, d domain.Delivery) (string, bool, error) {
	var target string
	var current bool
	var registeredBy, ownerID int64
	var ownerType string
	err := r.pool.QueryRow(ctx, `SELECT device.installation_id,device.registered_by,device.owner_type,device.owner_id,(device.is_active AND device.owner_type=n.recipient_type AND device.owner_id=n.recipient_id AND p.claim_token=$2::uuid AND p.status='PROCESSING' AND p.lease_until>clock_timestamp() AND n.status='PROCESSING' AND n.reason NOT IN ('offer_closed','order_terminal') AND n.expires_at>clock_timestamp() AND (n.kind<>'CourierOfferCreated' OR NOT EXISTS(SELECT FROM offer_states s WHERE s.offer_id=n.offer_id AND s.terminal))) FROM push_deliveries p JOIN notifications n ON n.id=p.notification_id JOIN device_registrations device ON device.id=p.device_id WHERE p.id=$1`, d.ID, d.Token).Scan(&target, &registeredBy, &ownerType, &ownerID, &current)
	if err == nil && current && r.authority != nil {
		current, err = r.authority.DeviceAuthorized(ctx, registeredBy, ownerType, ownerID)
		if err != nil {
			return "", false, err
		}
		if !current {
			_, err = r.pool.Exec(ctx, `UPDATE device_registrations SET is_active=false,installation_id='revoked-device:'||id,updated_at=clock_timestamp() WHERE id=$1`, d.DeviceID)
		}
	}
	return target, current, err
}
func finalize(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE notifications SET status=CASE WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1 AND status IN ('PENDING','PROCESSING')) THEN 'PROCESSING' WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1 AND status='FAILED') THEN 'FAILED' WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1 AND status='SENT') THEN 'SENT' ELSE 'CANCELLED' END,updated_at=clock_timestamp() WHERE id=$1`, id)
	return err
}
func (r *PushStore) FinishPush(ctx context.Context, d domain.Delivery, result domain.ProviderResult, delay time.Duration, maximum int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT id FROM notifications WHERE id=$1 FOR UPDATE`, d.NotificationID); err != nil {
		return err
	}
	status := domain.DeliveryStatus(result, d.Attempts, maximum)
	tag, err := tx.Exec(ctx, `UPDATE push_deliveries SET status=$3,last_code=$4,provider_message_id=$5,next_attempt_at=clock_timestamp()+$6*interval '1 millisecond',claim_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND claim_token=$2::uuid AND status='PROCESSING' AND lease_until>clock_timestamp()`, d.ID, d.Token, status, result.Code, result.MessageID, delay.Milliseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLeaseLost
	}
	if _, err = tx.Exec(ctx, `UPDATE push_attempts SET code=$3,provider_message_id=$4,finished_at=clock_timestamp() WHERE delivery_id=$1 AND attempt=$2`, d.ID, d.Attempts, result.Code, result.MessageID); err != nil {
		return err
	}
	if result.InvalidDevice {
		if _, err = tx.Exec(ctx, `UPDATE device_registrations SET is_active=false,installation_id='revoked-device:'||id,updated_at=clock_timestamp() WHERE id=$1`, d.DeviceID); err != nil {
			return err
		}
	}
	if err = finalize(ctx, tx, d.NotificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PushStore) Metrics(ctx context.Context) (string, error) {
	var created, sent, failed, retries, pending, inactive int64
	var age float64
	err := r.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notifications WHERE title<>''),(SELECT count(*) FROM push_attempts WHERE code='accepted'),(SELECT count(*) FROM push_attempts WHERE code NOT IN ('accepted','UNKNOWN','cancelled')),(SELECT count(*) FROM push_attempts WHERE attempt>1),(SELECT count(*) FROM notifications WHERE status IN ('PENDING','PROCESSING')),(SELECT count(*) FROM device_registrations WHERE NOT is_active),COALESCE((SELECT extract(epoch FROM clock_timestamp()-min(created_at)) FROM notifications WHERE status IN ('PENDING','PROCESSING')),0)`).Scan(&created, &sent, &failed, &retries, &pending, &inactive, &age)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("notifications_created_total %d\npush_accepted_total %d\npush_errors_total %d\npush_retries_total %d\nnotifications_pending %d\ndevices_inactive %d\nnotification_oldest_wait_seconds %.3f\n", created, sent, failed, retries, pending, inactive, age), nil
}
