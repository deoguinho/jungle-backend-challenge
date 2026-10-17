CREATE TABLE app.wallets (
    id UUID PRIMARY KEY,
    player_id UUID NOT NULL,
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance app.money_amount NOT NULL CHECK (balance >= 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),
    CONSTRAINT wallets_id_player_currency_uq UNIQUE (id, player_id, currency)
);
