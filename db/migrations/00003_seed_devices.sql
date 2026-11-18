-- +goose Up
INSERT INTO devices(id, name, kind) VALUES
    ('sensor-a1', 'Sensor Simulator A1', 'sensor'),
    ('adapter-b1', 'Interface Adapter B1', 'adapter'),
    ('meter-c1', 'Measurement Module C1', 'meter')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
-- Keep seeded devices on rollback: reservations may refer to them.
SELECT 1;
