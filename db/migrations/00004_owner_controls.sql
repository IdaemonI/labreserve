-- +goose Up
ALTER TABLE reservations ADD CONSTRAINT reservations_owner_no_controls
    CHECK (owner !~ '[[:cntrl:]]');

-- +goose Down
ALTER TABLE reservations DROP CONSTRAINT reservations_owner_no_controls;
