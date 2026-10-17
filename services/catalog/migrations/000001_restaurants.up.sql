CREATE TABLE restaurants (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200 AND char_length(name) <= 200),
    is_open BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Preserve the initial catalog data from the in-memory implementation.
INSERT INTO restaurants (name, is_open) VALUES
    ('Pizza House', true),
    ('Burger Corner', false);
