DO $$ BEGIN
 IF EXISTS(SELECT FROM deliveries WHERE delivery_address<>'') THEN RAISE EXCEPTION 'cannot discard delivery address snapshots; roll forward'; END IF;
END $$;
ALTER TABLE deliveries DROP COLUMN delivery_address;
