DO $$ BEGIN
 IF EXISTS(SELECT FROM restaurants WHERE address<>'') THEN RAISE EXCEPTION 'cannot discard configured restaurant addresses; roll forward'; END IF;
END $$;
ALTER TABLE restaurants DROP COLUMN address;
