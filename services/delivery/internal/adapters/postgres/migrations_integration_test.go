package postgres

import (
	"context"
	"os"
	"testing"
)

func TestDeliverySagaMigrationKeepsLegacyPOS(t *testing.T) {
	pool := integrationPool(t, "000001_delivery")
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO couriers(name,status,created_at) VALUES('Legacy','AVAILABLE',now());
 INSERT INTO deliveries(order_id,courier_id,status,amount_to_collect_minor,currency,version,order_version,ready,created_at,updated_at) VALUES(42,1,'DELIVERED',25000,'TJS',4,6,true,now(),now());
 INSERT INTO pos_confirmations(delivery_id,order_id,amount_minor,currency,payment_method,pos_reference,confirmed_by,confirmed_at) VALUES(1,42,25000,'TJS','POS_ON_DELIVERY','legacy-receipt',1,now());`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down", "up"} {
		body, err := os.ReadFile("../../../migrations/000002_saga." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(direction, err)
		}
	}
	addressSQL, err := os.ReadFile("../../../migrations/000003_delivery_address.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(addressSQL)); err != nil {
		t.Fatal(err)
	}
	dispatchSQL, err := os.ReadFile("../../../migrations/000005_automatic_dispatch.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(dispatchSQL)); err != nil {
		t.Fatal(err)
	}
	offerSQL, err := os.ReadFile("../../../migrations/000006_courier_offers.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(offerSQL)); err != nil {
		t.Fatal(err)
	}
	var offers int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM courier_offers`).Scan(&offers); err != nil || offers != 0 {
		t.Fatal("historical assignments invented offers", offers, err)
	}
	d, err := NewRepository(pool).Get(ctx, 0, 1)
	if err != nil || d.Confirmation == nil || d.Confirmation.ID <= 0 || d.Confirmation.ReceiptReference != "legacy-receipt" || d.Confirmation.AmountMinor != 25000 || !d.PreparationStarted {
		t.Fatal(d, err)
	}
}
