CREATE TABLE menu_categories (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    restaurant_id BIGINT NOT NULL,
    name TEXT NOT NULL CONSTRAINT menu_categories_name_check
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 200 AND char_length(name) <= 200),
    sort_order INTEGER NOT NULL DEFAULT 0 CONSTRAINT menu_categories_sort_order_check CHECK (sort_order >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT menu_categories_restaurant_fk FOREIGN KEY (restaurant_id) REFERENCES restaurants(id),
    CONSTRAINT menu_categories_restaurant_id_unique UNIQUE (restaurant_id, id)
);

CREATE TABLE menu_items (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    restaurant_id BIGINT NOT NULL,
    category_id BIGINT NOT NULL,
    name TEXT NOT NULL CONSTRAINT menu_items_name_check
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 200 AND char_length(name) <= 200),
    description TEXT NOT NULL DEFAULT '' CONSTRAINT menu_items_description_check CHECK (char_length(description) <= 2000),
    price_minor BIGINT NOT NULL CONSTRAINT menu_items_price_check CHECK (price_minor > 0),
    currency TEXT NOT NULL CONSTRAINT menu_items_currency_check CHECK (currency = 'TJS'),
    is_available BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,
    CONSTRAINT menu_items_restaurant_fk FOREIGN KEY (restaurant_id) REFERENCES restaurants(id),
    CONSTRAINT menu_items_category_fk FOREIGN KEY (restaurant_id, category_id)
        REFERENCES menu_categories(restaurant_id, id)
);

CREATE INDEX menu_categories_restaurant_order_idx ON menu_categories(restaurant_id, sort_order, id);
CREATE INDEX menu_items_active_category_idx ON menu_items(restaurant_id, category_id, id) WHERE deleted_at IS NULL;
