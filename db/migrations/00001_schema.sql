-- +goose Up
CREATE TABLE devices (
    id text PRIMARY KEY,
    name text NOT NULL,
    kind text NOT NULL,
    CONSTRAINT devices_id_length CHECK (char_length(id) BETWEEN 1 AND 64),
    CONSTRAINT devices_id_format CHECK (id ~ '^[a-z0-9][a-z0-9-]*$'),
    CONSTRAINT devices_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
    CONSTRAINT devices_kind_length CHECK (char_length(btrim(kind)) BETWEEN 1 AND 32)
);

CREATE TABLE reservations (
    id uuid PRIMARY KEY,
    device_id text NOT NULL REFERENCES devices(id),
    owner text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    CONSTRAINT reservations_owner_length CHECK (char_length(owner) BETWEEN 1 AND 100),
    CONSTRAINT reservations_owner_trimmed CHECK (owner = btrim(owner) AND owner !~ '^[[:space:]]*$'),
    CONSTRAINT reservations_release_order CHECK (released_at IS NULL OR released_at >= created_at)
);

CREATE UNIQUE INDEX one_active_reservation_per_device
    ON reservations(device_id) WHERE released_at IS NULL;

-- +goose Down
DROP TABLE reservations;
DROP TABLE devices;
