-- +goose Up
CREATE INDEX reservations_device_history_idx
    ON reservations(device_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX reservations_device_history_idx;
