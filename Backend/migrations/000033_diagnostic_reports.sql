CREATE TABLE diagnostic_reports (
    id SERIAL PRIMARY KEY,
    player_id TEXT NOT NULL,
    report TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now()
);
