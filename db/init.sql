-- Claude Gateway: PostgreSQL Schema
-- Requires: pgvector extension, pg_trgm extension
-- Run: psql -U gateway -d gateway -f init.sql

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ================================================================
-- AGENTS + CONVERSATIONS
-- Folder specialists and every user/assistant exchange (with embeddings).
-- ================================================================
-- Telegram specialists (@grey-website, @forex-trading, …) plus builtin/system
-- handles (default, cron, watcher, orchestrator). Auto-upserted on gateway start
-- and whenever a folder is created under .claude/agents/.
CREATE TABLE IF NOT EXISTS agents (
    id              SERIAL PRIMARY KEY,
    handle          TEXT NOT NULL UNIQUE,
    handle_key      TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    dir             TEXT,
    project_path    TEXT,
    framework       TEXT,
    source          TEXT NOT NULL DEFAULT 'folder',
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agents_handle_key ON agents (handle_key);
CREATE INDEX IF NOT EXISTS idx_agents_active ON agents (is_active) WHERE is_active = TRUE;

CREATE TABLE IF NOT EXISTS conversations (
    id              BIGSERIAL PRIMARY KEY,
    session_id      TEXT NOT NULL,
    agent_id        INT REFERENCES agents(id),
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content         TEXT NOT NULL,
    embedding       vector(768),
    token_estimate  INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_conv_session ON conversations (session_id);
CREATE INDEX IF NOT EXISTS idx_conv_created ON conversations (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_conv_agent ON conversations (agent_id);
-- ivfflat index for cosine similarity search
-- NOTE: requires at least 100 rows before becoming effective
CREATE INDEX IF NOT EXISTS idx_conv_embedding ON conversations
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- ================================================================
-- MEMORY ENTRIES
-- DB-backed bootstrap instructions, preferences, facts.
-- Replaces static bootstrap .md files over time.
-- ================================================================
CREATE TABLE IF NOT EXISTS memory_entries (
    id          SERIAL PRIMARY KEY,
    category    TEXT NOT NULL,
    -- categories: 'bootstrap'  = core instructions (always injected)
    --             'preference' = user preferences (always injected)
    --             'fact'       = learned facts (injected by relevance)
    --             'self'       = user profile info (always injected)
    key         TEXT,
    content     TEXT NOT NULL,
    importance  INT NOT NULL DEFAULT 5 CHECK (importance BETWEEN 1 AND 10),
    source      TEXT NOT NULL DEFAULT 'user',
    -- sources: 'user'   = explicit /remember
    --          'auto'   = extracted by post-processor
    --          'system' = set by gateway
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_memory_active ON memory_entries (is_active) WHERE is_active = TRUE;
CREATE INDEX IF NOT EXISTS idx_memory_category ON memory_entries (category);
CREATE INDEX IF NOT EXISTS idx_memory_importance ON memory_entries (importance DESC);

-- ================================================================
-- PEOPLE
-- ================================================================
CREATE TABLE IF NOT EXISTS people (
    id              SERIAL PRIMARY KEY,
    full_name       TEXT NOT NULL,
    preferred_name  TEXT,
    type            TEXT NOT NULL DEFAULT 'contact'
                    CHECK (type IN ('employee', 'client', 'vendor',
                                    'contractor', 'personal', 'contact')),
    role            TEXT,
    company         TEXT,
    notes           TEXT,
    embedding       vector(768),
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_people_active ON people (is_active) WHERE is_active = TRUE;
CREATE INDEX IF NOT EXISTS idx_people_name ON people USING gin (full_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_people_preferred ON people USING gin (preferred_name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_people_embedding ON people
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);

-- ================================================================
-- CONTACT DETAILS
-- ================================================================
CREATE TABLE IF NOT EXISTS contact_details (
    id           SERIAL PRIMARY KEY,
    person_id    INT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    contact_type TEXT NOT NULL
                 CHECK (contact_type IN ('email', 'phone', 'mobile',
                                         'telegram', 'address', 'other')),
    value        TEXT NOT NULL,
    label        TEXT,
    is_primary   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_contacts_person ON contact_details (person_id);

-- ================================================================
-- DAILY JOURNAL
-- AI-generated end-of-day summaries: what was discussed, decided,
-- actioned. Searchable by date and semantically by content.
-- ================================================================
CREATE TABLE IF NOT EXISTS daily_journal (
    id          SERIAL PRIMARY KEY,
    date        DATE NOT NULL UNIQUE,
    summary     TEXT NOT NULL,
    decisions   JSONB NOT NULL DEFAULT '[]',
    actions     JSONB NOT NULL DEFAULT '[]',
    topics      JSONB NOT NULL DEFAULT '[]',
    projects    JSONB NOT NULL DEFAULT '[]',
    stats       JSONB NOT NULL DEFAULT '{}',
    embedding   vector(768),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_journal_date ON daily_journal (date DESC);
CREATE INDEX IF NOT EXISTS idx_journal_embedding ON daily_journal
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);

-- ================================================================
-- USER PROFILE
-- Accumulated knowledge about the user: work patterns, style,
-- preferences, priorities. Grows over time through auto-learning.
-- ================================================================
CREATE TABLE IF NOT EXISTS user_profile (
    id          SERIAL PRIMARY KEY,
    aspect      TEXT NOT NULL UNIQUE,
    content     TEXT NOT NULL,
    evidence    JSONB NOT NULL DEFAULT '[]',
    confidence  REAL NOT NULL DEFAULT 0.5,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ================================================================
-- HELPER VIEWS
-- ================================================================

-- Active memory entries ordered for system prompt injection
CREATE OR REPLACE VIEW active_memory AS
    SELECT id, category, key, content, importance, source
    FROM memory_entries
    WHERE is_active = TRUE
    ORDER BY
        CASE category
            WHEN 'self' THEN 0
            WHEN 'bootstrap' THEN 1
            WHEN 'preference' THEN 2
            WHEN 'profile_doc' THEN 3
            WHEN 'fact' THEN 4
        END,
        importance DESC;

-- People with their contacts
CREATE OR REPLACE VIEW people_with_contacts AS
    SELECT
        p.*,
        COALESCE(
            json_agg(
                json_build_object(
                    'type', cd.contact_type,
                    'value', cd.value,
                    'label', cd.label,
                    'is_primary', cd.is_primary
                )
            ) FILTER (WHERE cd.id IS NOT NULL),
            '[]'::json
        ) AS contacts
    FROM people p
    LEFT JOIN contact_details cd ON cd.person_id = p.id
    WHERE p.is_active = TRUE
    GROUP BY p.id;

-- ================================================================
-- PERMISSIONS
-- When init.sql is run as superuser (postgres), the gateway role
-- needs explicit grants on all objects.
-- ================================================================
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gateway') THEN
        GRANT USAGE ON SCHEMA public TO gateway;
        GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO gateway;
        GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO gateway;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO gateway;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO gateway;
    END IF;
END
$$;
