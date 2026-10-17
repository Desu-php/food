-- Unknown historical addresses stay unknown; pickup is unavailable until configured.
ALTER TABLE restaurants ADD COLUMN address TEXT NOT NULL DEFAULT '' CHECK(address='' OR (char_length(btrim(address)) BETWEEN 1 AND 500 AND address=btrim(address)));
