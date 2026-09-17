CREATE TABLE public.nyro_control_state (
    singleton BIGINT PRIMARY KEY CHECK (singleton = 1),
    schema_version BIGINT NOT NULL CHECK (schema_version = 1),
    draft_revision BIGINT NOT NULL CHECK (draft_revision > 0),
    draft_json TEXT NOT NULL CHECK (octet_length(draft_json) BETWEEN 1 AND 1048576),
    published_revision BIGINT NOT NULL CHECK (published_revision > 0 AND published_revision <= draft_revision),
    published_json TEXT NOT NULL CHECK (octet_length(published_json) BETWEEN 1 AND 1048576)
);
