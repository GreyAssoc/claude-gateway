# Claude Gateway: Enhanced Memory & Context Spec (v2)

## Overview

This spec adds **persistent conversation memory**, **semantic search**, **people/contacts database**, **local LLM triage**, and **DB-backed bootstrap/instructions** to the existing Go gateway. The core problem being solved: **context loss**. Currently, when a Telegram session expires or compresses, all conversation history is lost. This spec ensures continuous memory by persisting every exchange to PostgreSQL and rebuilding context from DB on every message.

### Goals

1. **Never lose context** — every user/assistant exchange persisted with embeddings
2. **Semantic recall** — find relevant past conversations even weeks later
3. **Token economics** — local 7B model handles simple queries without burning Opus tokens
4. **People knowledge** — auto-inject relevant person context when names are mentioned
5. **DB-backed instructions** — bootstrap/preferences editable via Telegram, no SSH required
6. **Auto-learning** — extract and remember useful facts from conversations (with safeguards)
7. **Daily journal** — AI-generated end-of-day summaries of discussions, decisions, and actions
8. **User profiling** — accumulate knowledge about user's work patterns, communication style, priorities
9. **Smart system prompt** — only send full system prompt when SDK session is dead, not every message

---

## Architecture

```
Telegram Message
      ↓
Go Gateway
      ↓
[1] Pre-processor (Ollama 7B)
    - Can this be answered locally? (simple lookups, time, greetings)
    - Extract entity names (people, projects) for context lookup
    - Classify intent: simple (handle locally) vs complex (needs Claude)
      ↓
[2] Context Builder (PostgreSQL + pgvector)
    - Load DB-backed bootstrap instructions + user preferences
    - Lookup referenced people/contacts
    - Fetch last N conversation turns (recency layer)
    - pgvector semantic search for relevant older messages
    - Assemble system prompt + enriched user prompt
      ↓
[3] Route Decision
    ├── SIMPLE → Ollama 7B responds directly (no Claude tokens used)
    └── COMPLEX → Claude Code via sidecar (Opus for heavy lifting)
      ↓
[4] Post-processor (Ollama 7B)
    - Extract new facts/preferences worth remembering
    - Classify importance (1-10)
      ↓
[5] Persist to PostgreSQL
    - Save user message + assistant response with embeddings
    - Save any extracted facts to memory_entries
      ↓
Telegram Response
```

---

## Infrastructure

### PostgreSQL — Local Instance

Use the host's local PostgreSQL (not containerized). The gateway container connects via the Docker host network.

```yaml
# In gateway service environment:
- POSTGRES_URL=postgresql://gateway:${POSTGRES_PASSWORD}@host.docker.internal:5432/gateway?sslmode=disable
```

Requires pgvector extension installed on host:
```bash
# On Manjaro host
sudo pacman -S postgresql-libs
# Install pgvector extension (from AUR or source)
yay -S pgvector
```

Create database and user:
```sql
CREATE USER gateway WITH PASSWORD 'your_password';
CREATE DATABASE gateway OWNER gateway;
\c gateway
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
```

### Ollama — Docker Container

```yaml
  ollama:
    image: ollama/ollama:rocm          # ROCm variant for AMD GPU
    volumes:
      - ollama_data:/root/.ollama
    expose:
      - "11434"
    devices:
      - /dev/kfd
      - /dev/dri
    environment:
      - OLLAMA_KEEP_ALIVE=-1           # Keep models resident in VRAM permanently
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:11434/api/tags"]
      interval: 15s
      timeout: 5s
      retries: 5
      start_period: 30s
    restart: unless-stopped
```

Models to pull after first start:
```bash
docker exec ollama ollama pull nomic-embed-text      # ~274MB, embeddings
docker exec ollama ollama pull qwen2.5-coder:7b      # ~4.5GB, triage/extraction
```

VRAM budget on RX 6600 XT 8GB:
| Model | VRAM | Purpose |
|-------|------|---------|
| nomic-embed-text | ~600MB | Embedding generation (~50ms/call) |
| qwen2.5-coder:7b | ~4.5GB | Pre/post-processing (~1-3s/call) |
| **Total** | **~5.1GB** | **Leaves ~2.9GB headroom** |

### Docker Compose Additions

```yaml
  gateway:
    # existing service — add:
    environment:
      - POSTGRES_URL=postgresql://gateway:${POSTGRES_PASSWORD}@host.docker.internal:5432/gateway?sslmode=disable
      - OLLAMA_URL=http://ollama:11434
      - OLLAMA_EMBED_MODEL=nomic-embed-text
      - OLLAMA_LLM_MODEL=qwen2.5-coder:7b
    extra_hosts:
      - "host.docker.internal:host-gateway"
    depends_on:
      sidecar:
        condition: service_healthy
      ollama:
        condition: service_healthy

  ollama:
    # as above

volumes:
  ollama_data:
```

---

## PostgreSQL Schema

File: `db/init.sql`

```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ================================================================
-- CONVERSATIONS
-- Every user/assistant exchange, with embedding for semantic search.
-- This is the core persistence layer that solves context loss.
-- ================================================================
CREATE TABLE conversations (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content     TEXT NOT NULL,
    embedding   vector(768),
    token_estimate INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_conv_session   ON conversations (session_id);
CREATE INDEX idx_conv_created   ON conversations (created_at DESC);
CREATE INDEX idx_conv_embedding ON conversations
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- ================================================================
-- MEMORY ENTRIES
-- DB-backed bootstrap instructions, preferences, facts.
-- Replaces static bootstrap .md files over time.
-- ================================================================
CREATE TABLE memory_entries (
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
    -- sources: 'user' = explicit /remember
    --          'auto' = extracted by post-processor
    --          'system' = set by gateway
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_memory_active     ON memory_entries (is_active) WHERE is_active = TRUE;
CREATE INDEX idx_memory_category   ON memory_entries (category);
CREATE INDEX idx_memory_importance ON memory_entries (importance DESC);

-- ================================================================
-- PEOPLE
-- ================================================================
CREATE TABLE people (
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

CREATE INDEX idx_people_active    ON people (is_active) WHERE is_active = TRUE;
CREATE INDEX idx_people_name      ON people USING gin (full_name gin_trgm_ops);
CREATE INDEX idx_people_preferred ON people USING gin (preferred_name gin_trgm_ops);
CREATE INDEX idx_people_embedding ON people
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);

-- ================================================================
-- CONTACT DETAILS
-- ================================================================
CREATE TABLE contact_details (
    id           SERIAL PRIMARY KEY,
    person_id    INT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    contact_type TEXT NOT NULL
                 CHECK (contact_type IN ('email', 'phone', 'mobile',
                                         'telegram', 'address', 'other')),
    value        TEXT NOT NULL,
    label        TEXT,
    is_primary   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_contacts_person ON contact_details (person_id);

-- ================================================================
-- HELPER VIEWS
-- ================================================================

-- Active memory entries ordered for system prompt injection
CREATE VIEW active_memory AS
    SELECT id, category, key, content, importance, source
    FROM memory_entries
    WHERE is_active = TRUE
    ORDER BY
        CASE category
            WHEN 'self' THEN 0
            WHEN 'bootstrap' THEN 1
            WHEN 'preference' THEN 2
            WHEN 'fact' THEN 3
        END,
        importance DESC;

-- ================================================================
-- DAILY JOURNAL
-- AI-generated end-of-day summaries: what was discussed, decided,
-- actioned. Searchable by date and semantically by content.
-- ================================================================
CREATE TABLE daily_journal (
    id          SERIAL PRIMARY KEY,
    date        DATE NOT NULL UNIQUE,
    summary     TEXT NOT NULL,              -- AI-generated narrative summary
    decisions   JSONB NOT NULL DEFAULT '[]', -- [{decision, context, timestamp}]
    actions     JSONB NOT NULL DEFAULT '[]', -- [{action, status, project, timestamp}]
    topics      JSONB NOT NULL DEFAULT '[]', -- ["topic1", "topic2"]
    projects    JSONB NOT NULL DEFAULT '[]', -- ["ptp-dev", "grey"]
    stats       JSONB NOT NULL DEFAULT '{}', -- {messages, input_tokens, output_tokens, local_handled}
    embedding   vector(768),                -- for semantic search across days
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_journal_date      ON daily_journal (date DESC);
CREATE INDEX idx_journal_embedding ON daily_journal
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 50);

-- ================================================================
-- USER PROFILE
-- Accumulated knowledge about the user: work patterns, style,
-- preferences, priorities. Grows over time through auto-learning.
-- Categories are extensible — new aspects added as discovered.
-- ================================================================
CREATE TABLE user_profile (
    id          SERIAL PRIMARY KEY,
    aspect      TEXT NOT NULL UNIQUE,
    -- aspects: 'communication_style'  = how user writes emails, messages
    --          'query_style'          = how user asks questions to Claude
    --          'work_patterns'        = daily rhythm, when active, break patterns
    --          'priorities'           = current priorities, what matters most
    --          'job_description'      = role, responsibilities, reporting
    --          'projects_focus'       = which projects are active focus
    --          'email_style'          = tone, formality, sign-off, structure
    --          'decision_making'      = how user approaches decisions
    --          'pet_peeves'           = things to avoid in responses
    --          'tools_preferences'    = preferred tools, languages, approaches
    content     TEXT NOT NULL,
    evidence    JSONB NOT NULL DEFAULT '[]', -- [{observation, date, source}] — what led to this assessment
    confidence  REAL NOT NULL DEFAULT 0.5,    -- 0.0-1.0, increases with more evidence
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- People with their contacts
CREATE VIEW people_with_contacts AS
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
```

---

## New Go Packages

### 1. `internal/pgdb/pgdb.go` — PostgreSQL Client

```
Package: pgdb
Dependency: github.com/lib/pq (pure Go, no CGO)

Types:
  DB struct { pool *sql.DB }
  Conversation { ID, SessionID, Role, Content, CreatedAt }
  ConversationHit { Conversation + Similarity float64 }
  MemoryEntry { ID, Category, Key, Content, Importance, Source, IsActive }
  Person { ID, FullName, PreferredName, Type, Role, Company, Notes, IsActive }
  ContactDetail { ID, PersonID, ContactType, Value, Label, IsPrimary }
  PersonWithContacts { Person + Contacts []ContactDetail }

Functions:
  Open(connStr string) (*DB, error)
  Close() error

  // Conversations
  SaveConversation(ctx, sessionID, role, content string, embedding []float32, tokenEstimate int) error
  RecentConversations(ctx, sessionID string, limit int) ([]Conversation, error)
      Last N messages in this session (recency layer)
  RecentConversationsAllSessions(ctx, since time.Time, limit int) ([]Conversation, error)
      Recent messages across all sessions since a time (for cross-session continuity)
  SearchSimilar(ctx, embedding []float32, excludeIDs []int64, limit int, minSimilarity float64) ([]ConversationHit, error)
      Cosine similarity search, excluding IDs already in recency window

  // Memory entries
  SaveMemoryEntry(ctx, category, key, content, source string, importance int) (int, error)
  UpdateMemoryEntry(ctx, id int, content string, importance int) error
  DeactivateMemoryEntry(ctx, id int) error
  DeactivateByContent(ctx, contentFragment string) (int64, error)
      Deactivate entries matching ILIKE %fragment% — for /forget
  GetActiveMemory(ctx) ([]MemoryEntry, error)
      All active entries ordered by category priority + importance
  GetMemoryByCategory(ctx, category string) ([]MemoryEntry, error)
  CountAutoFacts(ctx) (int, error)
      Count auto-extracted facts (for pruning threshold)

  // People
  SavePerson(ctx, p *Person, embedding []float32) (int, error)
  UpdatePerson(ctx, id int, p *Person, embedding []float32) error
  DeletePerson(ctx, id int) error
  FindPersonByName(ctx, name string) (*PersonWithContacts, error)
      Trigram fuzzy match on full_name/preferred_name (similarity > 0.3)
  FindPersonByEmbedding(ctx, embedding []float32, minSimilarity float64) (*PersonWithContacts, error)
      Semantic fallback
  ListPeople(ctx) ([]PersonWithContacts, error)
  SaveContactDetail(ctx, personID int, contactType, value, label string) error

  // Daily journal
  UpsertJournalEntry(ctx, date string, entry *JournalEntry, embedding []float32) error
  GetJournalEntry(ctx, date string) (*JournalEntry, error)
  GetRecentJournalEntries(ctx, days int) ([]JournalEntry, error)
  SearchJournal(ctx, embedding []float32, limit int, minSimilarity float64) ([]JournalEntry, error)

  // User profile
  GetAllProfileAspects(ctx) ([]ProfileAspect, error)
  GetProfileAspect(ctx, aspect string) (*ProfileAspect, error)
  UpsertProfileAspect(ctx, aspect, content string, evidence []byte, confidence float64) error
  DeleteProfileAspect(ctx, aspect string) error
```

### 2. `internal/ollama/client.go` — Ollama Client (Embeddings + LLM)

```
Package: ollama
Dependencies: net/http, encoding/json (stdlib only)

Types:
  Client struct { baseURL, embedModel, llmModel string; httpClient *http.Client }

Functions:
  NewClient(baseURL, embedModel, llmModel string) *Client
  Health(ctx) error
      GET /api/tags — verify Ollama is reachable

  // Embedding
  Embed(ctx, text string) ([]float32, error)
      POST /api/embeddings with embedModel, returns 768-dim vector
  EmbedBatch(ctx, texts []string) ([][]float32, error)
      Sequential embedding of multiple texts

  // LLM inference
  Generate(ctx, prompt, systemPrompt string) (string, error)
      POST /api/generate with llmModel, stream=false
  GenerateJSON(ctx, prompt, systemPrompt string, result any) error
      Like Generate but parses response as JSON into result struct.
      Falls back to raw string extraction if JSON parse fails.
```

### 3. `internal/triage/triage.go` — Local LLM Pre-processor

```
Package: triage
Dependencies: internal/ollama, internal/pgdb

Types:
  Triager struct { llm *ollama.Client; db *pgdb.DB }

  Analysis struct {
      CanHandleLocally bool     // true = skip Claude, respond from 7B
      LocalResponse    string   // response text if handled locally
      PeopleNames      []string // extracted person names for context lookup
      Topics           []string // key topics for semantic search boosting
  }

Functions:
  NewTriager(llm *ollama.Client, db *pgdb.DB) *Triager

  Analyze(ctx, message string) (*Analysis, error)
      Sends message to 7B model with system prompt:
      "Classify this message. Return JSON:
       {
         "can_handle_locally": false,
         "local_response": null,
         "people_names": [],
         "topics": [],
         "reasoning": "..."
       }
       Set can_handle_locally=true ONLY for:
       - Greetings, thanks, small talk
       - Simple factual lookups from context (time, date)
       - Questions about remembered facts/people already in DB
       When in doubt, set false — let Claude handle it."

      If 7B model fails or returns unparseable response,
      returns Analysis{CanHandleLocally: false} — safe fallback.
```

**Conservative triage policy:** The 7B model is instructed to err on the side of passing to Claude. It should only handle locally when it's confident the response is correct and complete. Better to waste a few Claude tokens than give a bad local answer.

**Why triage works for token savings:**
- "hi", "thanks", "good morning" → 7B responds, zero Claude tokens
- "what's Sarah's email?" → 7B looks up people DB, responds directly
- "what time is it in London?" → 7B responds directly
- Anything code-related, complex, or ambiguous → Claude Opus

### 4. `internal/triage/postprocess.go` — Fact Extraction

```
Package: triage

Functions:
  ExtractFacts(ctx, userMessage, assistantResponse string) ([]ExtractedFact, error)
      Sends exchange to 7B model with system prompt:
      "Extract facts worth remembering long-term. Return JSON:
       {
         "facts": [
           { "key": "short_key", "content": "...", "category": "fact|preference|decision", "importance": 5 }
         ]
       }
       Rules:
       - Only extract CONCRETE facts (names, decisions, preferences, deadlines)
       - Do NOT extract vague observations or opinions
       - Do NOT extract anything already obvious from context
       - importance: 1-3=trivial, 4-6=useful, 7-8=important, 9-10=critical
       - Return empty facts array if nothing worth remembering
       - Maximum 3 facts per exchange"

Types:
  ExtractedFact struct {
      Key        string
      Content    string
      Category   string  // "fact", "preference", "decision"
      Importance int     // 1-10
  }
```

**Auto-learning safeguards:**
1. **Max 3 facts per exchange** — prevents flood
2. **Importance threshold** — only facts with importance >= 4 are saved
3. **Max auto-facts cap** — if auto-extracted facts exceed 200, prune lowest-importance entries
4. **Source tracking** — auto-facts tagged `source='auto'`, distinguishable from user-created
5. **User review** — `/memories auto` lists only auto-extracted facts for review
6. **User override** — `/forget` can remove any fact (auto or manual)
7. **Deduplication** — before saving, check if a similar fact already exists (key match)

### 5. `internal/context/builder.go` — Context Builder

```
Package: context
Dependencies: internal/pgdb, internal/ollama, internal/people

Types:
  Builder struct {
      db            *pgdb.DB
      embedder      *ollama.Client
      recentTurns   int       // default 20
      semanticHits  int       // default 5
      minSimilarity float64   // default 0.75
  }

  BuiltContext struct {
      SystemPrompt string     // bootstrap + self + preferences + instructions
      EnrichedPrompt string   // recent turns + semantic hits + people + user message
  }

Functions:
  NewBuilder(db, embedder, cfg) *Builder

  BuildSystemPrompt(ctx, sessionID string) (string, error)
      Assembles from DB:
        1. Self profile entries (category='self')
        2. User profile aspects (from user_profile table, confidence >= 0.4)
        3. Bootstrap instructions (category='bootstrap')
        4. User preferences (category='preference')
        5. Last 30 messages from conversations table (across all sessions,
           ordered by created_at DESC — provides continuity regardless of
           session boundaries or time gaps)
      Wrapped in XML sections with CDATA escaping.
      Called only when session detection determines SDK session is dead.

  BuildEnrichedPrompt(ctx, sessionID, userMessage string, peopleNames []string) (string, error)
      Assembles per-message context:
        1. Recent conversation turns (last N from conversations table)
        2. Semantic search hits (older relevant messages, excluding recency window)
        3. People context (looked up from peopleNames extracted by triager)
        4. Relevant auto-learned facts (semantic match against message)
      Prepended to user message in XML tags.

  RecordExchange(ctx, sessionID, userMessage, assistantResponse string) error
      Embeds + saves both messages to conversations table.
      Runs synchronously but called from a background goroutine by the bot.
```

**Smart system prompt via session detection:** The gateway checks if the SDK session is still alive in the sidecar before each message. If the session is live (idle < 30min), the system prompt is skipped — Claude already has it. If the session is dead or new, the full system prompt is sent. This avoids wasting tokens on repeat sends while guaranteeing Claude always has instructions when starting fresh. See the "Session Detection & Smart System Prompt" section for full details.

**Enriched prompt format:**
```xml
<conversation_history>
<turn role="user" time="14:30">Previous question about deployment</turn>
<turn role="assistant" time="14:31">Here's how to deploy...</turn>
<turn role="user" time="14:35">Follow-up about Docker</turn>
<turn role="assistant" time="14:36">The Docker config needs...</turn>
</conversation_history>

<related_past_conversations>
<hit similarity="0.89" date="2026-02-20">
[USER]: How do I set up Docker networking for the gateway?
[ASSISTANT]: The key issue was port mapping between containers...
</hit>
</related_past_conversations>

<people_context>
<person name="Sarah Thompson" role="Accountant" company="XYZ Firm">
Contact: sarah@xyz.com, 07700900000
Notes: Primary contact for billing queries
</person>
</people_context>

<remembered_facts>
- Prefer bullet points over paragraphs (preference, importance:7)
- Deployment server is 209.38.168.80 (fact, importance:8)
</remembered_facts>

<recent_journal>
[2026-02-24] Worked on gateway memory spec. Decided to use PostgreSQL + pgvector.
  Actions: Updated docker-compose (done), wrote init.sql (done), spec review (pending).
[2026-02-23] Grey website content pipeline. Published 3 articles. Fixed sitemap bug.
</recent_journal>

[actual user message here]
```

### 6. `internal/people/manager.go` — People Manager

```
Package: people
Dependencies: internal/pgdb, internal/ollama

Types:
  Manager struct { db *pgdb.DB; embedder *ollama.Client }

Functions:
  NewManager(db, embedder) *Manager
  AddPerson(ctx, fullName, preferredName, pType, role, company, notes string) (*Person, error)
      Generates embedding from concatenated fields, saves to DB
  UpdatePerson(ctx, id int, updates map[string]string) error
      Regenerates embedding after update
  RemovePerson(ctx, id int) error
  FindPerson(ctx, query string) (*PersonWithContacts, error)
      Trigram match → embedding fallback (>0.75 similarity)
  FindPeople(ctx, names []string) ([]PersonWithContacts, error)
      Batch lookup
  ListPeople(ctx) ([]PersonWithContacts, error)
  AddContact(ctx, personID int, contactType, value, label string) error
```

### 7. `internal/journal/journal.go` — Daily Journal

```
Package: journal
Dependencies: internal/pgdb, internal/ollama

Types:
  Journal struct { db *pgdb.DB; llm *ollama.Client; embedder *ollama.Client }

  DayEntry struct {
      Date      string
      Summary   string
      Decisions []Decision
      Actions   []Action
      Topics    []string
      Projects  []string
      Stats     DayStats
  }

  Decision struct { Decision, Context, Timestamp string }
  Action   struct { Action, Status, Project, Timestamp string }
  DayStats struct { Messages, InputTokens, OutputTokens, LocalHandled int }

Functions:
  NewJournal(db, llm, embedder) *Journal

  GenerateDailySummary(ctx, date string) (*DayEntry, error)
      1. Query all conversations for the given date from PostgreSQL
      2. Query auto-extracted facts created today
      3. Send the day's conversation history to the 7B model with prompt:
         "Summarize this day's work. Return JSON:
          {
            "summary": "narrative paragraph of what was discussed and accomplished",
            "decisions": [{"decision": "...", "context": "why", "timestamp": "HH:MM"}],
            "actions": [{"action": "...", "status": "done|pending|blocked", "project": "...", "timestamp": "HH:MM"}],
            "topics": ["topic1", "topic2"],
            "projects": ["project1"]
          }
          Focus on: what was decided, what was built/changed, what's pending.
          Be specific — include file names, feature names, error details."
      4. Generate embedding of the summary text
      5. Upsert into daily_journal table
      Falls back to simple stats summary if 7B fails.

  GetJournalEntry(ctx, date string) (*DayEntry, error)
  GetRecentEntries(ctx, days int) ([]DayEntry, error)
  SearchJournal(ctx, query string, limit int) ([]DayEntry, error)
      Semantic search across all journal entries using embedding similarity
  FormatJournalForContext(entries []DayEntry) string
      Formats recent journal entries for inclusion in enriched prompt
```

**When it runs:**
- Triggered by the existing `end_of_day` cron job (6pm weekdays, already configured)
- Also triggered by `memory_compaction` cron at midnight (catches any days missed)
- Can be triggered manually via `/journal` Telegram command
- Generates summary from PostgreSQL conversations (not SQLite session_history)

**Why use the 7B model, not Claude?**
- The day's conversations are already in PostgreSQL — no need to send them to Claude again
- The 7B model is free (local) and fast enough for summarization
- This runs at end-of-day, so 1-3s latency is irrelevant
- If 7B fails, falls back to simple stats (same as current compactor)

---

### 8. `internal/profile/profiler.go` — User Profiling

```
Package: profile
Dependencies: internal/pgdb, internal/ollama

Types:
  Profiler struct { db *pgdb.DB; llm *ollama.Client }

  ProfileAspect struct {
      Aspect     string
      Content    string
      Evidence   []Evidence
      Confidence float64
  }

  Evidence struct {
      Observation string
      Date        string
      Source      string  // "conversation", "explicit", "journal"
  }

Functions:
  NewProfiler(db, llm) *Profiler

  GetFullProfile(ctx) ([]ProfileAspect, error)
      Returns all profile aspects, sorted by confidence descending

  GetAspect(ctx, aspect string) (*ProfileAspect, error)
      Returns a single aspect

  SetAspect(ctx, aspect, content string) error
      Explicit user override (via /setprofile command)
      Sets confidence to 1.0, source="explicit"

  UpdateFromConversation(ctx, userMessage, assistantResponse string) error
      Called during post-processing (same pass as fact extraction).
      Sends exchange to 7B with prompt:
      "Given this exchange, update observations about the user's:
       - communication_style: how they phrase requests
       - query_style: how they approach Claude (brief vs detailed, technical level)
       - priorities: what seems important to them right now
       - work_patterns: any timing/rhythm observations
       Return JSON:
       {
         "observations": [
           {"aspect": "query_style", "observation": "Uses concise imperative sentences", "confidence_delta": 0.05}
         ]
       }
       Only include observations that are clearly supported by this exchange.
       Return empty observations if nothing notable."
      For each observation:
        1. Load existing aspect from DB
        2. Append evidence
        3. Adjust confidence (capped at 0.95 for auto-learned)
        4. If aspect doesn't exist, create with low confidence (0.3)
        5. If 5+ consistent observations, increase confidence significantly

  UpdateFromJournal(ctx, entry *DayEntry) error
      Runs after daily journal generation. Looks for patterns:
      - Which projects dominate the day → update projects_focus
      - Recurring topics → update priorities
      - Decision patterns → update decision_making

  FormatProfileForContext(ctx) string
      Formats profile for system prompt injection:
      "<user_profile>
       Communication style (confidence: 0.8): Concise, direct. Prefers bullet points...
       Current priorities (confidence: 0.7): Gateway enhancement, Grey website content...
       Query style (confidence: 0.85): Brief imperative commands. Expects code, not explanations...
       </user_profile>"
      Only includes aspects with confidence >= 0.4
```

**Profile aspects grow organically:**

The system starts with no profile. Over days/weeks of conversation, it accumulates observations:
- Day 1: "query_style" created with confidence 0.3 from 2 observations
- Day 5: "query_style" confidence 0.5 from 8 consistent observations
- Day 20: "query_style" confidence 0.85 — now reliably injected into system prompt

**User can always override:**
- `/setprofile query_style "I prefer concise responses with code examples"` → confidence 1.0
- `/profile` → view all aspects with confidence levels
- `/delprofile query_style` → remove an aspect entirely

**Profile vs memory_entries:**
- `memory_entries` stores discrete facts ("deployment server is X")
- `user_profile` stores continuous assessments ("user prefers bullet points")
- Profile aspects have confidence that increases with evidence
- Facts are binary (active/inactive)

---

## Session Detection & Smart System Prompt

### The Problem

The current gateway sends the system prompt only on the first message of a session (`SystemPromptSent` flag in `bot.go`). But the SDK session can die independently:
- Sidecar restarts → all sessions lost
- 30min idle timeout → session evicted
- Container OOM / crash → sessions lost

When this happens, the gateway still thinks the system prompt was sent (flag is true), but Claude has no context. The user experiences this as sudden context loss mid-conversation.

### The Solution: Sidecar Session Detection

Add a new WebSocket message type to check session liveness:

**Sidecar change** (`sidecar/index.js`):
```javascript
// Add new handler in the switch:
case "check-session":
    const sid = msg.session_id || "default";
    const sess = sessions.get(sid);
    send({
        type: "session-status",
        session_id: sid,
        alive: !!sess,
        sdk_session_id: sess?.sdkSessionId || null,
        last_active: sess ? new Date(sess.lastActive).toISOString() : null,
        idle_seconds: sess ? Math.floor((Date.now() - sess.lastActive) / 1000) : null,
    });
    break;
```

**Go gateway change** (`internal/agent/client.go`):
```go
// New method on Client:
type SessionStatus struct {
    Alive        bool
    SDKSessionID string
    LastActive   time.Time
    IdleSeconds  int
}

func (c *Client) CheckSession(ctx context.Context, sessionID string) (*SessionStatus, error)
    // Sends check-session message via WebSocket, waits for session-status response
```

**Bot flow change** (`internal/bot/commands.go`):

Replace the `SystemPromptSent` flag approach:

```go
func (b *Bot) processMessageWith(ctx context.Context, msg *tgbotapi.Message, prompt, systemPrompt string) {
    sessionID := fmt.Sprintf("tg-%d", msg.Chat.ID)

    // Check if SDK session is still alive in the sidecar
    needsSystemPrompt := true
    if systemPrompt == "" {
        // No explicit override — check if we need to build one
        status, err := b.agent.CheckSession(ctx, sessionID)
        if err == nil && status.Alive && status.IdleSeconds < 1800 {
            // SDK session is live and recent — skip system prompt
            needsSystemPrompt = false
        }
        // If check fails or session dead → send system prompt
    }

    opts := agent.AskOptions{
        SessionID: sessionID,
        Prompt:    prompt,  // enriched with context from PostgreSQL
        WorkDir:   b.cfg.WorkDir,
        Model:     b.cfg.Sidecar.DefaultModel,
    }

    if needsSystemPrompt {
        if systemPrompt != "" {
            opts.SystemPrompt = systemPrompt
        } else if b.contextBuilder != nil {
            sp, err := b.contextBuilder.BuildSystemPrompt(ctx)
            if err != nil {
                log.Printf("System prompt build failed: %v", err)
            } else {
                opts.SystemPrompt = sp
            }
        }
    }

    // ... rest of existing flow (streaming, progress, etc.) ...
}
```

**Key benefits:**
- System prompt only sent when needed (session dead or new conversation)
- No token waste on follow-up messages within a live session
- Automatic recovery when sidecar restarts
- No more stale `SystemPromptSent` flags that lie about session state
- The enriched user prompt (recent turns, semantic hits, people) is still sent every message — that's cheap and provides per-message context

**Session new/dead behaviour — recent conversation replay:**

When a new SDK session starts (session dead, sidecar restart, idle timeout), the system prompt includes the **last 30 messages** (not 30 minutes of wall-clock time) from the PostgreSQL `conversations` table. This means:
- If you chatted 5 minutes ago and the session expired → last 30 messages are right there
- If you come back after a weekend → last 30 messages from Friday's work are replayed
- If you've only sent 10 messages total → all 10 are included
- The count is messages, not turns — 30 messages = ~15 user/assistant exchanges

This is fetched via `pgdb.RecentConversationsAllSessions(ctx, limit=30)` which orders by `created_at DESC` across all session IDs — so even if the sidecar session ID changed, the conversation history is continuous.

The recent messages are formatted and included in the system prompt as a `<recent_conversation_history>` section so Claude picks up exactly where you left off:

```xml
<recent_conversation_history>
<!-- Last 30 messages replayed for continuity -->
<msg role="user" time="2026-02-25 14:30">Can you review the gateway spec?</msg>
<msg role="assistant" time="2026-02-25 14:31">I've read through the spec. Here are my thoughts...</msg>
<msg role="user" time="2026-02-25 14:35">Good points. Let's add daily journals.</msg>
<msg role="assistant" time="2026-02-25 14:36">I'll update the spec with a journal system...</msg>
</recent_conversation_history>
```

**What goes in system prompt vs enriched user prompt:**

| Layer | Where | When |
|-------|-------|------|
| Bootstrap instructions | System prompt | Only when session is new/dead |
| User profile | System prompt | Only when session is new/dead |
| Preferences | System prompt | Only when session is new/dead |
| Self profile | System prompt | Only when session is new/dead |
| Skills (if active) | System prompt | Only when session is new/dead |
| **Last 30 messages** | **System prompt** | **Only when session is new/dead** |
| Recent conversation turns | Enriched user prompt | Every message |
| Semantic search hits | Enriched user prompt | Every message |
| People context | Enriched user prompt | Every message (when names detected) |
| Relevant facts | Enriched user prompt | Every message |
| Journal context | Enriched user prompt | When relevant (semantic match) |

**Note on "Recent conversation turns" vs "Last 30 messages":** These serve different purposes. The **last 30 messages** in the system prompt give Claude full awareness of the recent conversation when starting a fresh session — this is the continuity layer. The **recent conversation turns** in the enriched user prompt are a smaller window (configurable, default 20) of the current active session that provides immediate conversational context on every message. When a session is live, only the enriched prompt turns are sent (the system prompt with the 30 messages was already sent at session start). When a session is new, both are sent — the 30-message replay in the system prompt ensures nothing is lost.

---

## Changes to Existing Code

### `internal/config/config.go` — New fields

```go
type Config struct {
    // ... existing fields ...
    PostgresURL      string        `yaml:"postgres_url"`
    OllamaURL        string        `yaml:"ollama_url"`
    OllamaEmbedModel string        `yaml:"ollama_embed_model"`
    OllamaLLMModel   string        `yaml:"ollama_llm_model"`
    Context          ContextConfig `yaml:"context"`
    Triage           TriageConfig  `yaml:"triage"`
}

type ContextConfig struct {
    RecentTurns    int     `yaml:"recent_turns"`      // default 20 (per-message enrichment)
    ReplayMessages int     `yaml:"replay_messages"`   // default 30 (system prompt on new session)
    SemanticHits   int     `yaml:"semantic_hits"`     // default 5
    MinSimilarity  float64 `yaml:"min_similarity"`    // default 0.75
}

type TriageConfig struct {
    Enabled        bool `yaml:"enabled"`             // default true
    AutoLearn      bool `yaml:"auto_learn"`           // default true
    MaxAutoFacts   int  `yaml:"max_auto_facts"`       // default 200
    MinImportance  int  `yaml:"min_importance"`       // default 4
}
```

### `configs/gateway.yaml` — New sections

```yaml
postgres_url: "${POSTGRES_URL}"
ollama_url: "${OLLAMA_URL:-http://ollama:11434}"
ollama_embed_model: "nomic-embed-text"
ollama_llm_model: "qwen2.5-coder:7b"

context:
  recent_turns: 20       # per-message enrichment (enriched user prompt)
  replay_messages: 30    # conversation replay on new session (system prompt)
  semantic_hits: 5
  min_similarity: 0.75

triage:
  enabled: true
  auto_learn: true
  max_auto_facts: 200
  min_importance: 4
```

### `cmd/gateway/main.go` — Startup wiring

Add after SQLite init, before bot creation:

```go
// PostgreSQL
pgDB, err := pgdb.Open(cfg.PostgresURL)
if err != nil {
    log.Fatalf("PostgreSQL connection failed: %v", err)
}
defer pgDB.Close()

// Ollama
ollamaClient := ollama.NewClient(cfg.OllamaURL, cfg.OllamaEmbedModel, cfg.OllamaLLMModel)
if err := ollamaClient.Health(ctx); err != nil {
    log.Printf("WARNING: Ollama unavailable: %v (triage + embeddings disabled)", err)
}

// People manager
peopleMgr := people.NewManager(pgDB, ollamaClient)

// Context builder
ctxBuilder := context.NewBuilder(pgDB, ollamaClient, cfg.Context)

// Triager
triager := triage.NewTriager(ollamaClient, pgDB)

// Daily journal
journalMgr := journal.NewJournal(pgDB, ollamaClient, ollamaClient)

// User profiler
profiler := profile.NewProfiler(pgDB, ollamaClient)

// Wire into bot
tgBot := bot.New(cfg, agentClient, limiter, database,
    // ... existing options ...
    bot.WithContextBuilder(ctxBuilder),
    bot.WithPeopleManager(peopleMgr),
    bot.WithTriager(triager),
    bot.WithJournal(journalMgr),
    bot.WithProfiler(profiler),
)
```

### `internal/bot/bot.go` — New fields + options

```go
type Bot struct {
    // ... existing fields ...
    contextBuilder *context.Builder
    peopleManager  *people.Manager
    triager        *triage.Triager
    journal        *journal.Journal
    profiler       *profile.Profiler
}

func WithContextBuilder(cb *context.Builder) Option { ... }
func WithPeopleManager(pm *people.Manager) Option { ... }
func WithTriager(t *triage.Triager) Option { ... }
func WithJournal(j *journal.Journal) Option { ... }
func WithProfiler(p *profile.Profiler) Option { ... }
```

### `internal/bot/commands.go` — Message flow changes

The `handleMessage` / `processMessageWith` flow changes to:

```go
func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
    // ... existing rate limit check ...

    // Step 1: Triage with local LLM (if available)
    if b.triager != nil && b.cfg.Triage.Enabled {
        analysis, err := b.triager.Analyze(ctx, msg.Text)
        if err == nil && analysis.CanHandleLocally && analysis.LocalResponse != "" {
            // Simple query handled by 7B — no Claude tokens used
            b.sendLongMessage(msg.Chat.ID, analysis.LocalResponse)
            // Still record the exchange for memory continuity
            if b.contextBuilder != nil {
                go b.contextBuilder.RecordExchange(
                    context.Background(), sessionID, msg.Text, analysis.LocalResponse)
            }
            return
        }
        // analysis.PeopleNames and analysis.Topics are passed to context builder
    }

    // Step 2: Build context from DB
    // System prompt: bootstrap + self + preferences (from PostgreSQL)
    // Enriched prompt: recent turns + semantic hits + people + facts
    // Both sent on EVERY message to ensure no context loss

    // Step 3: Call Claude via sidecar (existing flow)

    // Step 4: Post-process — extract facts + update profile (fire-and-forget)
    if b.triager != nil && b.cfg.Triage.AutoLearn {
        go func(user, assistant string) {
            bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()

            // Extract facts
            facts, err := b.triager.ExtractFacts(bgCtx, user, assistant)
            if err != nil {
                log.Printf("Fact extraction failed (non-fatal): %v", err)
            } else {
                for _, f := range facts {
                    if f.Importance >= b.cfg.Triage.MinImportance {
                        b.contextBuilder.db.SaveMemoryEntry(bgCtx,
                            f.Category, f.Key, f.Content, "auto", f.Importance)
                    }
                }
            }

            // Update user profile observations
            if b.profiler != nil {
                if err := b.profiler.UpdateFromConversation(bgCtx, user, assistant); err != nil {
                    log.Printf("Profile update failed (non-fatal): %v", err)
                }
            }
        }(msg.Text, resp.Text)
    }

    // Step 5: Record exchange for persistent memory
    if b.contextBuilder != nil {
        go b.contextBuilder.RecordExchange(
            context.Background(), sessionID, msg.Text, resp.Text)
    }
}
```

### New bot commands

```go
// In handleCommand switch:
case "addperson":
    b.handleAddPerson(ctx, msg)
case "people":
    b.handleListPeople(ctx, msg)
case "person":
    b.handleFindPerson(ctx, msg)
case "addcontact":
    b.handleAddContact(ctx, msg)
case "delperson":
    b.handleDelPerson(ctx, msg)
case "setbootstrap":
    b.handleSetBootstrap(ctx, msg)
case "listmemory":
    b.handleListMemory(ctx, msg)
case "journal":
    b.handleJournal(ctx, msg)
case "journal_search":
    b.handleJournalSearch(ctx, msg)
case "profile":
    b.handleProfile(ctx, msg)
case "setprofile":
    b.handleSetProfile(ctx, msg)
case "delprofile":
    b.handleDelProfile(ctx, msg)
```

### Migration of existing memory system

The existing `memory.Manager` (SQLite-backed) continues working during migration. New DB-backed memory runs in parallel. Over time, bootstrap `.md` files get migrated to `memory_entries` rows with `category='bootstrap'`. The transition is gradual:

1. **Phase 1:** Both systems active. System prompt = existing memory.Manager output + PostgreSQL memory entries. Deduplicated by content.
2. **Phase 2:** Migrate bootstrap `.md` content into PostgreSQL `memory_entries`. Remove `.md` files.
3. **Phase 3:** Retire `memory.Manager`. All memory from PostgreSQL.

During Phase 1, the context builder merges both sources, with PostgreSQL entries taking precedence if there's overlap.

---

## Telegram Commands (Complete)

### Existing (unchanged)
| Command | Description |
|---------|-------------|
| `/start` | Show available commands |
| `/status` | Gateway health & rate limits |
| `/reset` | Clear Claude session |
| `/cost` | Token usage this session |
| `/cron` | Active scheduled jobs |
| `/skills` | List loaded skills |
| `/remember <text>` | Save a memory (now writes to PostgreSQL) |
| `/forget <text>` | Deactivate matching memories |
| `/memories` | List all active memories |
| `/today` | Today's summary |
| `/context` | Current context layers |

### New
| Command | Description |
|---------|-------------|
| `/addperson <desc>` | Add person: `/addperson Sarah Thompson \| accountant \| XYZ Firm \| billing contact \| email:sarah@xyz.com` |
| `/people` | List all people |
| `/person <name>` | Find person by name (fuzzy match) |
| `/addcontact <name> <type> <value>` | Add contact: `/addcontact Sarah mobile 07700900000` |
| `/delperson <id>` | Remove person |
| `/setbootstrap <text>` | Add/update bootstrap instruction |
| `/listmemory [category]` | List memories, optionally filtered: `/listmemory auto`, `/listmemory bootstrap` |
| `/journal [date]` | Show journal entry: `/journal` (today), `/journal 2026-02-20` |
| `/journal_search <query>` | Search journal entries semantically |
| `/profile` | Show accumulated user profile (all aspects with confidence) |
| `/setprofile <aspect> <content>` | Override a profile aspect: `/setprofile email_style "Formal, sign off with 'Kind regards'"` |
| `/delprofile <aspect>` | Remove a profile aspect |

### Modified behaviour
| Command | Change |
|---------|--------|
| `/remember` | Now writes to PostgreSQL `memory_entries` instead of SQLite `memory_curated` |
| `/forget` | Now deactivates in PostgreSQL by content fragment match (ILIKE) |
| `/memories` | Now reads from PostgreSQL, shows source (user/auto) and importance |

---

## Graceful Degradation

| Component Down | Behaviour |
|----------------|-----------|
| PostgreSQL unreachable | **Fatal on startup** — PostgreSQL is required for core memory. Gateway will not start without it. |
| Ollama unreachable | Skip triage (all messages go to Claude). Skip embeddings (conversations saved with NULL embedding, no semantic search). Skip auto-learning. People lookup falls back to name-only (no semantic). |
| Ollama LLM model not loaded | Same as Ollama down for triage/auto-learning. Embeddings still work if embed model is loaded. |
| Ollama embed model not loaded | No embeddings generated. Conversations saved without vectors. Semantic search returns no results. |

---

## Implementation Order

### Phase 1: Infrastructure
1. Install pgvector on host PostgreSQL
2. Create gateway database, user, run `init.sql`
3. Add Ollama to `docker-compose.yml`
4. Pull both models (`nomic-embed-text`, `qwen2.5-coder:7b`)
5. Add env vars to `.env`
6. Verify connectivity from gateway container to host Postgres and Ollama

### Phase 2: Core packages (additive — no changes to existing files)
7. `internal/pgdb/pgdb.go` — PostgreSQL client with all CRUD
8. `internal/ollama/client.go` — Ollama embedding + LLM client
9. `internal/people/manager.go` — People CRUD + lookup
10. `internal/context/builder.go` — System prompt + enriched prompt builder
11. `internal/triage/triage.go` — Message analysis + routing
12. `internal/triage/postprocess.go` — Fact extraction
13. `internal/journal/journal.go` — Daily journal generation + search
14. `internal/profile/profiler.go` — User profile accumulation

### Phase 3: Integration (changes to existing files)
15. Config additions (`config.go`, `gateway.yaml`)
16. Add `check-session` handler to sidecar (`sidecar/index.js`)
17. Add `CheckSession` method to agent client (`agent/client.go`)
18. Startup wiring (`main.go`)
19. Bot options + message flow changes (`bot.go`, `commands.go`)
20. Smart system prompt via session detection (replace `SystemPromptSent` flag)
21. New command handlers (people, journal, profile, memory)
22. `/remember` and `/forget` migration to PostgreSQL
23. Wire journal generation into existing `end_of_day` cron job
24. Wire profile update into post-processing pipeline

### Phase 4: Memory migration
25. Export existing SQLite `memory_curated` to PostgreSQL `memory_entries`
26. Export bootstrap `.md` content to PostgreSQL `memory_entries` (category='bootstrap')
27. Run both systems in parallel, verify parity
28. Remove SQLite memory tables + `memory.Manager` (deferred — only after confidence)

### Phase 5: Testing
29. End-to-end: send messages, verify PostgreSQL recording + embeddings
30. Session detection: verify system prompt sent only when session is dead
31. Semantic search: discuss a topic, then ask about it days later
32. Triage: verify simple queries handled locally, complex ones go to Claude
33. Auto-learning: verify facts extracted with appropriate importance
34. People: add, find, reference in conversation
35. Journal: verify end-of-day summary generation, search past days
36. Profile: verify observations accumulate, confidence increases over time
37. Bootstrap: modify via Telegram, verify next fresh session uses updated instructions
38. Degradation: stop Ollama, verify Claude-only mode works

---

## Dependencies

### New Go dependencies
| Package | Purpose | CGO? |
|---------|---------|------|
| `github.com/lib/pq` | PostgreSQL driver | No — pure Go |

One new dependency. Everything else uses stdlib (`net/http`, `encoding/json`, `database/sql`).

### Resource budget
| Component | RAM | VRAM | Disk |
|-----------|-----|------|------|
| PostgreSQL (host) | ~100-256MB | — | Grows with conversations (~1KB/message) |
| Ollama | ~200MB | ~5.1GB | ~4.8GB (models) |
| Gateway (Go) | ~50MB | — | Existing |
| Sidecar (Node.js) | ~150MB | — | Existing |

---

## Key Design Decisions

**PostgreSQL is required, not optional.** The core value proposition is persistent memory. Making it optional would mean the primary feature doesn't work. Ollama is optional — triage and auto-learning are enhancements, not essentials.

**System prompt on every message.** The current "send once, rely on SDK cache" approach is the root cause of context loss. Sending bootstrap instructions on every message costs a few hundred extra tokens but guarantees Claude always has full context. The trade-off is worth it.

**Conservative triage.** The 7B model should only handle locally when highly confident. False positives (handling complex queries locally) are worse than false negatives (sending simple queries to Claude). The triage prompt explicitly says "when in doubt, let Claude handle it."

**Auto-learning with guardrails.** Facts are auto-extracted but filtered by importance threshold (>=4), capped at 200 total, and tagged with `source='auto'` for easy review/cleanup. Users can review with `/listmemory auto` and remove with `/forget`.

**Local PostgreSQL, containerized Ollama.** PostgreSQL is persistent, stateful data that benefits from host-level management (backups, monitoring, pg_dump). Ollama is a stateless inference server that's easier to manage in Docker (model files in a volume, easy to rebuild/upgrade).

**Pipe-delimited `/addperson` format.** Deterministic parsing instead of LLM-based. The 7B model could parse natural language, but for data entry commands where correctness matters, explicit structure is more reliable. The triage model is reserved for classification tasks where occasional errors are tolerable.

**Gradual memory migration.** Both SQLite memory and PostgreSQL memory run in parallel during transition. No big-bang migration. The old system keeps working until the new one is proven.

**Daily journal uses the 7B model.** The day's conversations are already in PostgreSQL — sending them to Claude for summarization would be redundant token spend. The 7B model summarizes locally for free. If it produces a poor summary, the raw conversations are still searchable via semantic search.

**User profile is evidence-based.** Profile aspects aren't guessed from a single interaction. Each observation is stored as evidence with a confidence delta. Confidence grows slowly (0.05 per consistent observation) and caps at 0.95 for auto-learned aspects. Users can override any aspect to confidence 1.0. Only aspects with confidence >= 0.4 are injected into the system prompt.

**Session detection over flags.** The `SystemPromptSent` flag was a lie — it tracked what the gateway sent, not what the SDK retained. Checking the sidecar's session store directly tells the truth. This costs one extra WebSocket round-trip (~1ms) but eliminates an entire class of context loss bugs.

**Journal entries are searchable.** Each journal entry gets an embedding of its summary text. When a user asks "what did we work on last week?" or "when did we discuss the Docker networking issue?", semantic search across journal entries finds relevant days quickly — much faster than scanning raw conversation history.
