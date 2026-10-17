-- Historical deliveries keep their unknown location; new commands carry a snapshot.
ALTER TABLE deliveries ADD COLUMN delivery_address TEXT NOT NULL DEFAULT '' CHECK(delivery_address='' OR (char_length(btrim(delivery_address)) BETWEEN 1 AND 500 AND delivery_address=btrim(delivery_address)));
