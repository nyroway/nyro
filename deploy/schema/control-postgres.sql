CREATE TABLE public.upstreams (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    balance TEXT NOT NULL,
    targets JSONB NOT NULL
);
CREATE TABLE public.models (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    capability TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES public.upstreams(uid),
    access JSONB NOT NULL,
    execution JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.mcps (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES public.upstreams(uid),
    allowed_tools JSONB NOT NULL,
    access JSONB NOT NULL,
    execution JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.consumers (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    credentials JSONB NOT NULL,
    grants JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.nyro_schema (version INTEGER PRIMARY KEY CHECK (version = 2));
INSERT INTO public.nyro_schema VALUES (2);
