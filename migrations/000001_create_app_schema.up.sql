CREATE SCHEMA app;

CREATE DOMAIN app.money_amount AS NUMERIC
    CONSTRAINT money_amount_scale CHECK (scale(VALUE) <= 2 AND VALUE = trunc(VALUE, 2))
    CONSTRAINT money_amount_magnitude CHECK (abs(VALUE) < 1000000000000000000::numeric);
