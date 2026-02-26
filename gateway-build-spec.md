# Telegram → Claude Code Gateway: Full Build Specification

## Overview

This document specifies all changes and additions to the existing Telegram-to-Claude-Code gateway. The goal is to implement long-term hybrid memory, employee/contact knowledge, persistent commands, and local LLM pre/post processing — replacing the function of CLAUDE.md with a dynamic, persistent PostgreSQL-backed memory system.

---

## Architecture Overview

```
Telegram Message
      ↓
Gateway (Node.js / Python)
      ↓
[1] Pre-processor (local Ollama LLM)
    - Entity extraction (who/what is mentioned)
    - Intent detection (is this a /command or normal message?)
    - Compress/summarise input data if large
      ↓
[2] Context Builder
    - Load bootstrap + active memory entries
    - Lookup extracted entities in people/contacts DB
    - Fetch last N conversation turns (recency layer)
    - pgvector semantic search for relevant older messages
    - Assemble final system prompt
      ↓
[3] Claude Code (via existing gateway mechanism)
      ↓
[4] Post-processor (local Ollama LLM)
    - Validate output quality
    - Check for obvious errors
    - Extract any new facts/entities to remember
      ↓
[5] Save exchange to PostgreSQL (with embedding)
      ↓
Telegram Response
```

---

## Docker Compose Stack

Add the following services alongside the existing gateway container:

```yaml
version: '3.8'

services:
  gateway:
    # existing service — add environment variables below
    environment:
      - POSTGRES_URL=postgresql://gateway:password@postgres:5432/gateway_memory
      - OLLAMA_URL=http://ollama:11434
      - OLLAMA_EMBED_MODEL=nomic-embed-text
      - OLLAMA_LLM_MODEL=qwen2.5-coder:7b
      - SESSION_TIMEOUT_HOURS=4
      - RECENT_TURNS=30
      - SEMANTIC_HITS=5
    depends_on:
      - postgres
      - ollama

  postgres:
    image: pgvector/pgvector:pg16
    environment:
      POSTGRES_DB: gateway_memory
      POSTGRES_USER: gateway
      POSTGRES_PASSWORD: password
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./db/init.sql:/docker-entrypoint-initdb.d/init.sql
    ports:
      - "5432:5432"

  ollama:
    image: ollama/ollama
    volumes:
      - ollama_data:/root/.ollama
    ports:
      - "11434:11434"
    # For AMD GPU on Manjaro host:
    devices:
      - /dev/kfd
      - /dev/dri
    environment:
      - OLLAMA_KEEP_ALIVE=-1  # Keep models resident in VRAM permanently
    # Pull models on first start via entrypoint or manually:
    # docker exec ollama ollama pull nomic-embed-text
    # docker exec ollama ollama pull qwen2.5-coder:7b

volumes:
  postgres_data:
  ollama_data:
```

> **Note on AMD GPU passthrough**: The `devices` block above enables ROCm GPU access for Ollama on Manjaro. If GPU passthrough causes issues, remove those lines — Ollama will fall back to CPU, which is still fast enough for embedding and 7B models on the i9-9900X.

---

## Database Schema

Create file `db/init.sql`:

```sql
-- Enable pgvector extension
CREATE EXTENSION IF NOT EXISTS vector;

-- ============================================================
-- SESSIONS
-- ============================================================
CREATE TABLE sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  started_at TIMESTAMPTZ DEFAULT NOW(),
  ended_at TIMESTAMPTZ,
  trigger VARCHAR(50)  -- 'new_command', 'timeout', 'startup'
);

-- ============================================================
-- CONVERSATIONS
-- All messages, user and assistant, with embeddings
-- ============================================================
CREATE TABLE conversations (
  id SERIAL PRIMARY KEY,
  session_id UUID REFERENCES sessions(id),
  role VARCHAR(20) NOT NULL,           -- 'user' | 'assistant' | 'system'
  content TEXT NOT NULL,
  turn_number INT,                      -- sequential turn counter per session
  embedding vector(768),               -- nomic-embed-text dimension
  token_estimate INT,
  created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX ON conversations(created_at DESC);
CREATE INDEX ON conversations(session_id);
CREATE INDEX ON conversations USING ivfflat (embedding vector_cosine_ops)
  WITH (lists = 100);

-- ============================================================
-- BOOTSTRAP & MEMORY ENTRIES
-- Replaces CLAUDE.md — injected at session start
-- ============================================================
CREATE TABLE memory_entries (
  id SERIAL PRIMARY KEY,
  category VARCHAR(50) NOT NULL,
  -- categories: 'bootstrap', 'command', 'preference',
  --             'fact', 'project', 'self'
  key VARCHAR(200),
  content TEXT NOT NULL,
  is_active BOOLEAN DEFAULT TRUE,
  priority INT DEFAULT 5,              -- lower = injected first (1-10)
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Core bootstrap entries — populate these with your existing CLAUDE.md content
-- INSERT INTO memory_entries (category, key, content, priority)
-- VALUES ('bootstrap', 'coding_standards', '...', 1);

-- ============================================================
-- PEOPLE & CONTACTS
-- ============================================================
CREATE TABLE people (
  id SERIAL PRIMARY KEY,
  full_name VARCHAR(200) NOT NULL,
  preferred_name VARCHAR(100),         -- nickname / first name for matching
  type VARCHAR(50),
  -- types: 'employee', 'client', 'vendor', 'contractor', 'personal'
  role VARCHAR(200),
  department VARCHAR(100),
  company VARCHAR(200),
  notes TEXT,                          -- freeform, injected when person referenced
  embedding vector(768),               -- embedding of name+role+notes for fuzzy lookup
  is_active BOOLEAN DEFAULT TRUE,
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX ON people USING ivfflat (embedding vector_cosine_ops)
  WITH (lists = 50);

CREATE TABLE contact_details (
  id SERIAL PRIMARY KEY,
  person_id INT REFERENCES people(id) ON DELETE CASCADE,
  contact_type VARCHAR(50),
  -- types: 'email', 'phone', 'mobile', 'telegram', 'address', 'other'
  value TEXT NOT NULL,
  label VARCHAR(100),                  -- 'work', 'personal', 'mobile'
  is_primary BOOLEAN DEFAULT FALSE
);

CREATE TABLE relationships (
  id SERIAL PRIMARY KEY,
  person_a_id INT REFERENCES people(id) ON DELETE CASCADE,
  person_b_id INT REFERENCES people(id) ON DELETE CASCADE,
  relationship VARCHAR(100),
  -- e.g. 'reports_to', 'manages', 'works_with', 'client_of', 'contact_at'
  notes TEXT
);

-- ============================================================
-- SELF — personal details about the user
-- One record, always injected as context
-- ============================================================
CREATE TABLE self_profile (
  id SERIAL PRIMARY KEY,
  key VARCHAR(100) UNIQUE,
  value TEXT,
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Example entries to populate:
-- INSERT INTO self_profile (key, value) VALUES ('name', 'Steve');
-- INSERT INTO self_profile (key, value) VALUES ('timezone', 'Europe/London');
-- INSERT INTO self_profile (key, value) VALUES ('company', 'Grey & Associates');
-- INSERT INTO self_profile (key, value) VALUES ('preferences', 'Respond concisely. Prefer bullet points for lists. Always show file paths.');

-- ============================================================
-- PROJECTS
-- ============================================================
CREATE TABLE projects (
  id SERIAL PRIMARY KEY,
  name VARCHAR(200) NOT NULL,
  description TEXT,
  status VARCHAR(50) DEFAULT 'active',  -- 'active', 'paused', 'complete'
  notes TEXT,
  embedding vector(768),
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX ON projects USING ivfflat (embedding vector_cosine_ops)
  WITH (lists = 50);

-- ============================================================
-- HELPER VIEWS
-- ============================================================

-- Quick view: last 30 conversation turns
CREATE VIEW recent_turns AS
  SELECT id, session_id, role, content, turn_number, created_at
  FROM conversations
  ORDER BY id DESC
  LIMIT 60;  -- 30 exchanges = 60 rows

-- Quick view: all active memory for session start
CREATE VIEW session_context AS
  SELECT category, key, content, priority
  FROM memory_entries
  WHERE is_active = TRUE
  ORDER BY priority ASC, category ASC;
```

---

## Gateway Code Changes

### 1. Session Management

```javascript
// session.js

const SESSION_TIMEOUT_HOURS = parseInt(process.env.SESSION_TIMEOUT_HOURS) || 4;

async function getOrCreateSession(db) {
  // Find most recent session
  const last = await db.query(`
    SELECT id, started_at FROM sessions
    ORDER BY started_at DESC LIMIT 1
  `);

  if (last.rows.length === 0) {
    return createSession(db, 'startup');
  }

  const lastSession = last.rows[0];
  const hoursSince = (Date.now() - new Date(lastSession.started_at)) / 3600000;

  if (hoursSince > SESSION_TIMEOUT_HOURS) {
    // End old session, start new one
    await db.query(`UPDATE sessions SET ended_at = NOW() WHERE id = $1`, [lastSession.id]);
    return createSession(db, 'timeout');
  }

  return lastSession.id;
}

async function createSession(db, trigger) {
  const result = await db.query(`
    INSERT INTO sessions (trigger) VALUES ($1) RETURNING id
  `, [trigger]);
  return result.rows[0].id;
}

// Call on container startup to record restart
async function recordStartup(db) {
  return createSession(db, 'startup');
}
```

### 2. Embedding Service

```javascript
// embeddings.js

const OLLAMA_URL = process.env.OLLAMA_URL || 'http://ollama:11434';
const EMBED_MODEL = process.env.OLLAMA_EMBED_MODEL || 'nomic-embed-text';

async function getEmbedding(text) {
  const response = await fetch(`${OLLAMA_URL}/api/embeddings`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model: EMBED_MODEL, prompt: text })
  });
  const data = await response.json();
  return data.embedding;  // float array of length 768
}

// Format for PostgreSQL pgvector insert
function embeddingToSQL(embedding) {
  return `[${embedding.join(',')}]`;
}
```

### 3. Pre-Processor (Local LLM)

```javascript
// preprocessor.js

const OLLAMA_URL = process.env.OLLAMA_URL || 'http://ollama:11434';
const LLM_MODEL = process.env.OLLAMA_LLM_MODEL || 'qwen2.5-coder:7b';

async function ollamaGenerate(prompt, systemPrompt) {
  const response = await fetch(`${OLLAMA_URL}/api/generate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model: LLM_MODEL,
      prompt,
      system: systemPrompt,
      stream: false,
      format: 'json'
    })
  });
  const data = await response.json();
  try {
    return JSON.parse(data.response);
  } catch {
    return data.response;
  }
}

async function extractEntities(message) {
  // Returns: { people: [], projects: [], isCommand: bool, commandType: str, topics: [] }
  return await ollamaGenerate(
    `Extract entities from this message: "${message}"`,
    `You extract structured information from messages. Return JSON only with these fields:
    {
      "people": ["name1", "name2"],       // any person names mentioned
      "projects": ["project1"],           // any project names mentioned
      "topics": ["topic1"],               // key topics/technologies mentioned
      "isCommand": false,                  // true if this looks like a /remember, /forget, /addperson etc command
      "commandType": null,                 // "remember", "forget", "addperson", "addcontact", "setbootstrap", "new" etc
      "commandPayload": null               // the content of the command if applicable
    }
    Return only valid JSON. No explanation.`
  );
}

async function summariseForContext(text, maxTokens = 500) {
  // Compress large input data before sending to Claude
  if (text.length < 2000) return text;  // No need to summarise short content

  return await ollamaGenerate(
    `Summarise this content concisely, preserving all important facts, numbers, names, and technical details:\n\n${text}`,
    `You are a precise summariser. Keep all important details. Return plain text, no JSON.`
  );
}

async function extractNewFacts(userMessage, assistantResponse) {
  // After Claude responds, extract any new facts worth remembering
  return await ollamaGenerate(
    `User said: "${userMessage}"\nAssistant said: "${assistantResponse}"`,
    `Extract any new facts, decisions, or preferences worth remembering long-term.
    Return JSON: { "facts": [{ "key": "short_key", "content": "fact to remember", "category": "fact|preference|decision" }] }
    Return empty facts array if nothing worth remembering. Return only valid JSON.`
  );
}
```

### 4. Context Builder

```javascript
// contextBuilder.js

async function buildContext(db, message, entities, sessionId) {
  const parts = [];

  // --- SELF PROFILE ---
  const selfProfile = await db.query(`SELECT key, value FROM self_profile`);
  if (selfProfile.rows.length > 0) {
    const selfLines = selfProfile.rows.map(r => `${r.key}: ${r.value}`).join('\n');
    parts.push(`## About the User\n${selfLines}`);
  }

  // --- BOOTSTRAP & MEMORY ENTRIES ---
  const memoryEntries = await db.query(`
    SELECT category, key, content FROM memory_entries
    WHERE is_active = TRUE
    ORDER BY priority ASC, category ASC
  `);

  const byCategory = {};
  for (const row of memoryEntries.rows) {
    if (!byCategory[row.category]) byCategory[row.category] = [];
    byCategory[row.category].push(row.content);
  }

  for (const [cat, items] of Object.entries(byCategory)) {
    parts.push(`## ${cat.charAt(0).toUpperCase() + cat.slice(1)}\n${items.join('\n')}`);
  }

  // --- PEOPLE MENTIONED ---
  if (entities.people && entities.people.length > 0) {
    const peopleContext = await lookupPeople(db, entities.people, message);
    if (peopleContext) parts.push(`## People Referenced\n${peopleContext}`);
  }

  // --- PROJECTS MENTIONED ---
  if (entities.projects && entities.projects.length > 0) {
    const projectContext = await lookupProjects(db, entities.projects);
    if (projectContext) parts.push(`## Projects Referenced\n${projectContext}`);
  }

  // --- RECENT CONVERSATION TURNS (recency layer) ---
  const recentTurns = await db.query(`
    SELECT role, content, created_at FROM conversations
    WHERE session_id = $1 OR created_at > NOW() - INTERVAL '4 hours'
    ORDER BY id DESC
    LIMIT $2
  `, [sessionId, parseInt(process.env.RECENT_TURNS) * 2 || 60]);

  const turnHistory = recentTurns.rows.reverse().map(r =>
    `[${r.role.toUpperCase()}]: ${r.content}`
  ).join('\n\n');

  // --- SEMANTIC SEARCH (relevance layer) ---
  // Find older relevant messages not already in recent turns
  const embedding = await getEmbedding(message);
  const minRecentId = recentTurns.rows.length > 0
    ? Math.min(...recentTurns.rows.map(r => r.id))
    : 999999999;

  const semanticHits = await db.query(`
    SELECT role, content, created_at,
           1 - (embedding <=> $1::vector) AS similarity
    FROM conversations
    WHERE id < $2
      AND embedding IS NOT NULL
    ORDER BY embedding <=> $1::vector
    LIMIT $3
  `, [embeddingToSQL(embedding), minRecentId, parseInt(process.env.SEMANTIC_HITS) || 5]);

  if (semanticHits.rows.length > 0) {
    const semanticContext = semanticHits.rows
      .filter(r => r.similarity > 0.75)  // Only include high-relevance hits
      .map(r => `[${r.created_at.toLocaleDateString()} - ${r.role.toUpperCase()}]: ${r.content}`)
      .join('\n\n');
    if (semanticContext) {
      parts.push(`## Relevant Past Context\n${semanticContext}`);
    }
  }

  return {
    systemPrompt: parts.join('\n\n---\n\n'),
    turnHistory
  };
}

async function lookupPeople(db, names, originalMessage) {
  const results = [];

  for (const name of names) {
    // First try exact/partial name match
    const exact = await db.query(`
      SELECT p.*, array_agg(cd.contact_type || ': ' || cd.value) as contacts
      FROM people p
      LEFT JOIN contact_details cd ON cd.person_id = p.id
      WHERE is_active = TRUE
        AND (full_name ILIKE $1 OR preferred_name ILIKE $1)
      GROUP BY p.id
      LIMIT 1
    `, [`%${name}%`]);

    if (exact.rows.length > 0) {
      results.push(formatPerson(exact.rows[0]));
      continue;
    }

    // Fall back to semantic search
    const embedding = await getEmbedding(name);
    const semantic = await db.query(`
      SELECT p.*, array_agg(cd.contact_type || ': ' || cd.value) as contacts,
             1 - (p.embedding <=> $1::vector) AS similarity
      FROM people p
      LEFT JOIN contact_details cd ON cd.person_id = p.id
      WHERE p.is_active = TRUE AND p.embedding IS NOT NULL
      GROUP BY p.id
      ORDER BY p.embedding <=> $1::vector
      LIMIT 1
    `, [embeddingToSQL(embedding)]);

    if (semantic.rows.length > 0 && semantic.rows[0].similarity > 0.8) {
      results.push(formatPerson(semantic.rows[0]));
    }
  }

  return results.join('\n\n');
}

function formatPerson(person) {
  const lines = [
    `**${person.full_name}**${person.preferred_name ? ` (${person.preferred_name})` : ''}`,
    person.role ? `Role: ${person.role}` : null,
    person.department ? `Department: ${person.department}` : null,
    person.company ? `Company: ${person.company}` : null,
    person.notes ? `Notes: ${person.notes}` : null,
  ].filter(Boolean);

  if (person.contacts && person.contacts[0]) {
    lines.push(`Contact: ${person.contacts.filter(c => c !== 'null: null').join(', ')}`);
  }

  return lines.join('\n');
}

async function lookupProjects(db, projectNames) {
  const results = [];
  for (const name of projectNames) {
    const result = await db.query(`
      SELECT name, description, status, notes FROM projects
      WHERE name ILIKE $1 AND status != 'complete'
      LIMIT 1
    `, [`%${name}%`]);
    if (result.rows.length > 0) {
      const p = result.rows[0];
      results.push(`**${p.name}** (${p.status})\n${p.description || ''}\n${p.notes || ''}`);
    }
  }
  return results.join('\n\n');
}
```

### 5. Command Handler

```javascript
// commandHandler.js

async function handleCommand(db, commandType, payload, message) {
  switch (commandType) {

    case 'remember':
      // /remember [content] — save as a memory entry
      await db.query(`
        INSERT INTO memory_entries (category, key, content)
        VALUES ('command', $1, $2)
      `, [slugify(payload.substring(0, 50)), payload]);
      return `✅ Remembered: "${payload}"`;

    case 'forget':
      // /forget [key or content fragment] — deactivate matching memory
      await db.query(`
        UPDATE memory_entries SET is_active = FALSE
        WHERE content ILIKE $1
      `, [`%${payload}%`]);
      return `✅ Forgotten entries matching: "${payload}"`;

    case 'addperson':
      // /addperson [natural language description]
      // Parse via local LLM then insert
      const personData = await parsePerson(payload);
      return await savePerson(db, personData);

    case 'addcontact':
      // /addcontact [name] [type] [value]
      return await saveContact(db, payload);

    case 'setbootstrap':
      // /setbootstrap [content] — update bootstrap
      await db.query(`
        INSERT INTO memory_entries (category, key, content, priority)
        VALUES ('bootstrap', 'custom_' || extract(epoch from now()), $1, 1)
      `, [payload]);
      return `✅ Bootstrap updated`;

    case 'new':
      // /new — force new session
      await db.query(`UPDATE sessions SET ended_at = NOW() WHERE ended_at IS NULL`);
      return `✅ New session started. Previous context archived.`;

    case 'listmemory':
      const entries = await db.query(`
        SELECT category, key, content FROM memory_entries
        WHERE is_active = TRUE ORDER BY category, priority
      `);
      return entries.rows.map(r => `[${r.category}] ${r.key}: ${r.content}`).join('\n');

    case 'addproject':
      const projData = await parseProject(payload);
      return await saveProject(db, projData);

    default:
      return null;  // Not a recognised command, pass to Claude
  }
}

async function parsePerson(naturalLanguage) {
  // Use local LLM to parse "Sarah is our accountant at XYZ, her mobile is 0412..."
  return await ollamaGenerate(
    `Parse this into structured person data: "${naturalLanguage}"`,
    `Extract person details and return JSON:
    {
      "full_name": "",
      "preferred_name": "",
      "type": "employee|client|vendor|contractor|personal",
      "role": "",
      "department": "",
      "company": "",
      "notes": "",
      "contacts": [{ "type": "email|phone|mobile|telegram", "value": "", "label": "work|personal" }]
    }
    Return only valid JSON.`
  );
}

async function savePerson(db, personData) {
  const embedding = await getEmbedding(
    `${personData.full_name} ${personData.role} ${personData.company} ${personData.notes}`
  );

  const result = await db.query(`
    INSERT INTO people (full_name, preferred_name, type, role, department, company, notes, embedding)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector)
    RETURNING id
  `, [
    personData.full_name, personData.preferred_name, personData.type,
    personData.role, personData.department, personData.company,
    personData.notes, embeddingToSQL(embedding)
  ]);

  const personId = result.rows[0].id;

  for (const contact of (personData.contacts || [])) {
    await db.query(`
      INSERT INTO contact_details (person_id, contact_type, value, label)
      VALUES ($1, $2, $3, $4)
    `, [personId, contact.type, contact.value, contact.label]);
  }

  return `✅ Person saved: ${personData.full_name}`;
}
```

### 6. Message Saver

```javascript
// messageSaver.js

async function saveMessage(db, sessionId, role, content, turnNumber) {
  const embedding = await getEmbedding(content);
  const tokenEstimate = Math.ceil(content.length / 4);

  await db.query(`
    INSERT INTO conversations
      (session_id, role, content, turn_number, embedding, token_estimate)
    VALUES ($1, $2, $3, $4, $5::vector, $6)
  `, [
    sessionId, role, content, turnNumber,
    embeddingToSQL(embedding), tokenEstimate
  ]);
}
```

### 7. Main Gateway Flow

```javascript
// gateway.js (main message handler — replace/extend existing handler)

async function handleMessage(telegramMessage) {
  const db = getDB();
  const sessionId = await getOrCreateSession(db);
  const text = telegramMessage.text;

  // --- Step 1: Pre-process with local LLM ---
  const entities = await extractEntities(text);

  // --- Step 2: Handle commands without calling Claude ---
  if (entities.isCommand && entities.commandType) {
    const commandResult = await handleCommand(
      db, entities.commandType, entities.commandPayload, text
    );
    if (commandResult) {
      await sendTelegram(commandResult);
      return;
    }
  }

  // --- Step 3: Build context ---
  const { systemPrompt, turnHistory } = await buildContext(db, text, entities, sessionId);

  // --- Step 4: Save user message ---
  const turnNumber = await getNextTurnNumber(db, sessionId);
  await saveMessage(db, sessionId, 'user', text, turnNumber);

  // --- Step 5: Call Claude Code ---
  const claudeResponse = await callClaudeCode(systemPrompt, turnHistory, text);

  // --- Step 6: Post-process with local LLM ---
  const newFacts = await extractNewFacts(text, claudeResponse);
  if (newFacts.facts && newFacts.facts.length > 0) {
    for (const fact of newFacts.facts) {
      await db.query(`
        INSERT INTO memory_entries (category, key, content)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `, [fact.category || 'fact', fact.key, fact.content]);
    }
  }

  // --- Step 7: Save assistant response ---
  await saveMessage(db, sessionId, 'assistant', claudeResponse, turnNumber);

  // --- Step 8: Send to Telegram ---
  await sendTelegram(claudeResponse);
}
```

---

## Session Start Behaviour

On every new session (startup, timeout, or `/new` command):

1. Query all active `memory_entries` ordered by priority
2. Query `self_profile`
3. Build a system prompt from these — this replaces CLAUDE.md
4. Prepend this as the system message before the first user message

This means bootstrap instructions, remembered commands, and preferences are always injected fresh into every new Claude Code session automatically, regardless of how Claude Code's internal compaction works.

---

## Telegram Commands Reference

| Command | Action |
|---|---|
| `/remember [text]` | Save a persistent instruction or fact |
| `/forget [text]` | Deactivate memory entries matching text |
| `/addperson [description]` | Add a person in plain English |
| `/addcontact [name] [type] [value]` | Add contact detail to existing person |
| `/addproject [description]` | Add a project |
| `/setbootstrap [content]` | Add a bootstrap instruction |
| `/new` | Force a new session |
| `/listmemory` | Show all active memory entries |

---

## Data Population Guide

### Populate self_profile

```sql
INSERT INTO self_profile (key, value) VALUES
  ('name', 'Steve'),
  ('timezone', 'Europe/London'),
  ('company', 'Grey & Associates'),
  ('location', 'London / Wareham'),
  ('preferences', 'Respond concisely. Show file paths. Prefer bullet points.');
```

### Migrate CLAUDE.md content

For each section of your existing CLAUDE.md, insert as a `memory_entry`:

```sql
INSERT INTO memory_entries (category, key, content, priority) VALUES
  ('bootstrap', 'coding_standards', 'Always use TypeScript. Prefer async/await.', 1),
  ('bootstrap', 'project_structure', 'Gateway lives in /app/gateway. DB in /app/db.', 2),
  ('command', 'always_respond_in_english', 'Always respond in English.', 3);
```

### Add employees

Either use the `/addperson` Telegram command:
```
/addperson Sarah Thompson is our accountant at XYZ Firm, her work email is sarah@xyz.com and mobile is 07700900000
```

Or insert directly:
```sql
INSERT INTO people (full_name, preferred_name, type, role, company, notes)
VALUES ('Sarah Thompson', 'Sarah', 'client', 'Accountant', 'XYZ Firm', 'Primary contact for all billing queries');

INSERT INTO contact_details (person_id, contact_type, value, label)
VALUES (1, 'email', 'sarah@xyz.com', 'work'),
       (1, 'mobile', '07700900000', 'work');
```

---

## Ollama Model Setup

Run these after starting the stack:

```bash
# Pull embedding model (~274MB)
docker exec ollama ollama pull nomic-embed-text

# Pull local LLM for pre/post processing (~4.5GB at 4-bit)
docker exec ollama ollama pull qwen2.5-coder:7b

# Verify both are loaded
docker exec ollama ollama list
```

To keep both models resident in VRAM permanently (recommended given your RX 6600 XT 8GB):

```bash
# In docker-compose.yml already set: OLLAMA_KEEP_ALIVE=-1
# Both models together use ~5GB, leaving 3GB headroom
```

---

## Key Design Decisions

**Message count not wall-clock time for recency**: The `RECENT_TURNS` env var controls how many turns back to include, not a time window. Overnight gaps don't lose context.

**Bootstrap replaces CLAUDE.md**: All CLAUDE.md content migrates into `memory_entries` table with `category = 'bootstrap'`. Gateway injects these at every new session start, so Claude Code always starts with full context regardless of its internal session state.

**Dual-layer retrieval**: Recent turns (exact recency) + pgvector semantic hits (relevance). The semantic layer excludes messages already in the recent window to avoid duplication.

**Local LLM is non-blocking for quality**: If Ollama is unavailable, the gateway should fall through gracefully and call Claude directly with basic context. The local LLM enhances but does not block.

**Entity extraction drives context injection**: Only inject people/project context that is actually relevant to the current message, keeping the system prompt lean and focused.

**Embeddings on every message**: Both user and assistant messages get embedded on save, making the full history semantically searchable over time.

---

## Environment Variables Reference

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_URL` | — | PostgreSQL connection string |
| `OLLAMA_URL` | `http://ollama:11434` | Ollama API base URL |
| `OLLAMA_EMBED_MODEL` | `nomic-embed-text` | Embedding model name |
| `OLLAMA_LLM_MODEL` | `qwen2.5-coder:7b` | Local LLM for pre/post processing |
| `SESSION_TIMEOUT_HOURS` | `4` | Hours of inactivity before new session |
| `RECENT_TURNS` | `30` | Number of conversation turns to include as recency context |
| `SEMANTIC_HITS` | `5` | Number of semantic search results to include |
| `SIMILARITY_THRESHOLD` | `0.75` | Minimum cosine similarity for semantic hits |

---

## Implementation Order (Suggested)

1. Spin up PostgreSQL and run `init.sql`
2. Spin up Ollama and pull both models
3. Implement embedding service and verify it connects
4. Implement message saver — start writing all messages to DB
5. Implement session management
6. Migrate CLAUDE.md into `memory_entries` table
7. Implement context builder (bootstrap + recent turns only, no semantic yet)
8. Implement command handler for `/remember`, `/new`, `/addperson`
9. Add semantic search layer
10. Add pre-processor entity extraction
11. Add post-processor fact extraction
12. Populate people and contacts
13. Test end-to-end

This order means the system is usable and improving after each step rather than requiring everything to be complete before any benefit is seen.
