---
name: schedule-of-condition
description: Process SoC audio recordings into formatted legal documents
command: soc
---

# Schedule of Condition Skill

Process audio recordings of Schedule of Condition surveys into formatted legal documents.

## Trigger Phrases
- "process SoC audio"
- "schedule of condition"
- "process the SoC recordings"
- "transcribe the surveys"

## Prerequisites
- **Gemini API key** in environment variable `GEMINI_API_KEY` (loaded from `/workspace/projects/claude-gateway/.env`)
- **Google Drive access** via Google Workspace MCP tool (for downloading audio files)
- **process_soc_audio.py** at `/workspace/projects/grey-docs/00-engine/process_soc_audio.py`
- **SoC template** at `/workspace/projects/grey-docs/00-engine/templates/14.1 - Schedule of Condition TEMPLATE.docx`
- **Vocabulary file** at `/workspace/projects/grey-docs/00-engine/transcription-vocabulary.txt`
- **Python deps** installed via `pip --user` (openpyxl, python-docx, google-generativeai)

## Key Directories
- **Working dir:** `/workspace/projects/grey-docs/`
- **Engine:** `00-engine/` (scripts, templates, vocab)
- **Output:** `00-audio_output/` (generated docs, transcripts, moved audio files)
- **Client projects:** `/clients/current/` (READ ONLY — copy DATA files, never modify)

## Audio Source

**Google Drive folder:** `1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw`

Audio files named by date/time (e.g. `260210_1050.mp3`). Property address is spoken at the start of each recording.

## Running the Script

### Usage
```bash
cd /workspace/projects/grey-docs

# Full mode — auto-detects AO from transcript address
python3 00-engine/process_soc_audio.py <audio_file>

# Full mode — manual AO number
python3 00-engine/process_soc_audio.py <audio_file> <ao_number>

# Reprocess from cached transcript (no Gemini API call)
python3 00-engine/process_soc_audio.py <audio_file> --skip-transcription

# Standalone mode (no DATA file needed)
python3 00-engine/process_soc_audio.py <audio_file> --standalone

# Dry run
python3 00-engine/process_soc_audio.py <audio_file> --dry-run
```

### Arguments
| Argument | Required | Description |
|----------|----------|-------------|
| `audio_file` | Yes | Path to audio file (mp3, m4a, wav) |
| `ao_number` | No | AO number (1-20). Auto-detected if omitted |
| `--standalone` | No | Generate without DATA file |
| `--skip-transcription` | No | Reuse existing raw transcript RTF |
| `--dry-run` | No | Preview without generating files |

## Workflow — Process ONE File at a Time

### Step 1: Download Audio from Google Drive
Use the Google Workspace MCP tool to list and download files from the SoC audio folder:

**Google Drive folder ID:** `1MnC-0QWn1P7bxgUuMZtw2DPq7OhuAgjw`

Download audio files to `/workspace/projects/grey-docs/` using the MCP tool.

### Step 2: Find Project & Copy DATA File
Search current clients for a project matching the property:
```bash
find /clients/current/ -maxdepth 1 -iname "*<road_name>*" -type d
```

Copy the DATA file (only one at a time in the working directory):
```bash
rm -f /workspace/projects/grey-docs/00.0\ -\ DATA*
cp "/clients/current/<project-dir>/00.0 - DATA - ....xlsx" \
   /workspace/projects/grey-docs/
```

### Step 3: Run the Script
```bash
cd /workspace/projects/grey-docs
python3 00-engine/process_soc_audio.py <audio_file>
```

The script handles everything automatically:
1. Transcribes audio via Gemini API
2. Extracts property address from transcript opening
3. Auto-detects which AO matches the address
4. Processes transcript through deterministic pipeline
5. Generates formatted Word document
6. Extracts Award Notes / Conditional Consent Notes to separate files
7. Saves raw transcript RTF for verification
8. Moves audio to output directory

### Step 4: Report Results
Report the generated files and key details (property, AO, BO, date, weather).

## Deterministic Processing Pipeline

All processing is handled by explicit Python code — **no AI inference** except for the initial Gemini transcription. The pipeline order is fixed:

### 1. Transcription (Gemini API — only inference step)
- Gemini transcribes audio **exactly as spoken**, including verbal punctuation commands
- Verbal commands are kept as-is (e.g. "full stop", "new line") — NOT interpreted by Gemini
- Output is raw text with verbal cues preserved

### 2. Clean Filler Words
- Removes "uh", "um", "er" and similar filler words via regex

### 3. Apply Vocabulary Corrections
- **File:** `transcription-vocabulary.txt` (~390 entries)
- Format: `"pattern": "replacement"` — applied longest-first
- Handles:
  - Verbal punctuation → actual punctuation ("full stop" → `. `, "comma" → `, `)
  - Section headers → HTML tags ("external observations" → `<b><u>EXTERNAL OBSERVATIONS</u></b>`)
  - Common misspellings ("seller" → "cellar", "on suite" → "En-Suite")
  - Surveying terminology corrections
  - Generic "stroke" → "/" rule for composite room names

### 4. Structural Marker Conversion (Regex)
After vocabulary corrections, regex rules convert remaining "new line" / "new paragraph" markers that vocab couldn't catch (handles all punctuation variants):
```
"full stop. New line," → ".<p></p>"
"New paragraph,"       → ".<br><p></p>"
```
- Period is preserved before breaks if one existed (sentences end with full stops)
- No period added before headings (room names, floor levels)
- Cleans up `, .` → `.` and `..` → `.` artefacts

### 5. Apply Corrections
Handles "all caps correction" verbal editing cue:
- **Full rephrase:** replaces the entire preceding sentence
- **Partial edit (splice):** searches backwards for matching word and splices correction into original sentence

### 6. Apply Level and Room Formatting
Detects headings **only when they appear as standalone lines** (preceded by a break AND followed by `<p></p>` or end of text):

| Type | Detection | Format |
|------|-----------|--------|
| Floor levels | `FLOOR_LEVELS` list (Cellar → Fourth Floor) | `<b>Bold</b>` |
| Floor levels + "Continued" | Same list + "Continued" suffix | `<b>Bold</b>` (merged later) |
| Room names | `ROOM_NAMES` list (100+ entries incl. composites) | `<u>Underline</u>` |
| "Stairs between X and Y" | Dynamic regex pattern | `<u>Underline</u>` (title-cased) |
| Elevation subheadings | Rear, Front, Side, Left/Right Side, Roof, Terrace | `<u>Underline</u>` |

**Critical rule:** A name is ONLY formatted as a heading when it appears standalone on a line. "Hallway door opens..." is NOT formatted — only "Hallway" on its own line becomes a heading.

### 7. Reorganize Content
- Separates content into: External Observations, Internal Observations, Award Notes, Conditional Consent Notes
- **Merges "continued" levels:** If surveyor returns to a level (e.g. "Ground Floor" then later "Ground Floor Continued"), all rooms are grouped under one "Ground Floor" heading
- Adds bullet points (`•`) to all observation lines
- External Observations placed first, then Internal Observations
- **Award Notes** extracted to separate text file (not included in SoC document)
- **Conditional Consent Notes/Conditions** extracted to separate text file (not included in SoC document)

### 8. Document Generation
- Bullet point cleanup: strips leading `,` `.` `;` `:` and capitalises first letter
- Hanging indent on bullets: 0.5" left, -0.25" first line
- Section headers: 12pt, bold + underline + ALL CAPS
- Floor levels: 11pt, bold, space before 12pt
- Room names: 11pt, underline, space before 8pt
- Observations: 11pt bullet points

## AO Auto-Detection

When `ao_number` is not specified, the script:
1. Extracts the property address from the transcript opening ("Schedule of condition of [ADDRESS]")
2. Strips filler words from the extracted address
3. Compares against all AO sheet addresses in the DATA file:
   - **Primary match:** Same street number + same road type (road/street/place/avenue etc.)
   - **Fallback match:** Same street number only (if exactly one AO has that number)
4. Handles Gemini mishearings (e.g. "Five Vice Road" matches "5 Weiss Road" — number 5 + road type "road")

## Output Files

All saved to `00-audio_output/`:

| File | Description |
|------|-------------|
| `14.1 - Schedule of Condition ({property}).docx` | Formatted SoC document |
| `{base}_raw_transcript.rtf` | Raw dictation for human verification |
| `{base}.mp3` | Original audio (moved from input) |
| `Award Notes ({property}).txt` | Extracted award notes (if any) |
| `Conditional Consent Notes ({property}).txt` | Extracted conditional consent notes (if any) |

## Date & Weather Extraction

Both extracted from raw transcript via regex:
- **Date:** Matches "date is [date]" with tolerance for filler words, commas, variations
- **Weather:** Matches "weather is [conditions]" — common values: "overcast and dry", "overcast and damp"

## Important Rules
- **Process ONE audio file at a time** — don't batch
- **Read-only on `/clients/current/`** — copy DATA file, never modify original
- **One DATA file at a time** in the working directory
- **Deterministic code only** — all formatting, corrections, and structure handled by explicit Python logic; AI inference only for initial Gemini transcription
- **Raw transcript is the audit trail** — always saved for verification against the formatted document
- **Verbal cues are explicit** — don't infer structure from context, only use explicit markers
- **Gemini free tier limit:** 20 requests/day — bottleneck for batch processing
- **`--skip-transcription`** reuses cached RTF — avoids Gemini API calls when reprocessing
