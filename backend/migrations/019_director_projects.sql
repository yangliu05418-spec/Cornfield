-- +goose Up
CREATE TABLE director_projects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    document jsonb,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX director_projects_owner_updated_idx
    ON director_projects(owner_user_id, updated_at DESC, id DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON director_projects TO studio_api;
GRANT DELETE ON director_projects TO studio_worker;

-- +goose Down
REVOKE ALL ON director_projects FROM studio_worker, studio_api;
DROP TABLE director_projects;
