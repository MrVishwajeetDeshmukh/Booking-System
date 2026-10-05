CREATE TABLE shows (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    price_paise BIGINT NOT NULL CHECK (price_paise >= 0),
    total_seats INTEGER NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE TABLE seats (
    show_id UUID NOT NULL REFERENCES shows(id),
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('available', 'held', 'confirmed')),
    user_id TEXT,
    reservation_id UUID,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    PRIMARY KEY (show_id, name)
);

CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    reservation_id UUID NOT NULL UNIQUE,
    show_id UUID NOT NULL REFERENCES shows(id),
    user_id TEXT NOT NULL,
    request_body JSONB NOT NULL,
    amount_paise BIGINT NOT NULL DEFAULT 0 CHECK (amount_paise >= 0),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX seats_expired_holds_idx
    ON seats (updated_at, show_id, user_id)
    WHERE status = 'held';

CREATE TABLE user_show_limits (
    show_id UUID NOT NULL REFERENCES shows(id),
    user_id TEXT NOT NULL,
    seats_booked INTEGER NOT NULL CHECK (seats_booked >= 0),
    PRIMARY KEY (show_id, user_id)
);
