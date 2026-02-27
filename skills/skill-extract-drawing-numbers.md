---
name: extract-drawing-numbers
description: Extract drawing register from PDF plans and populate DATA spreadsheet
command: extract-drawings
---

# Drawing Register Extraction Skill

Deterministic extraction of drawing information from architectural/structural PDF plans.

**Two modes:**
1. **Text Register** - Produces standardised text file with drawing list
2. **DATA Population** - Extracts and populates drawings directly into project DATA spreadsheet

## Trigger Phrases

This skill activates when you ask to:
- "extract drawing numbers from [plans/pdf]"
- "get drawing register from [plans/pdf]"
- "pull drawing numbers from [plans/pdf]"
- "list drawings in [plans/pdf]"
- "parse drawing numbers from [plans/pdf]"
- "read drawing register from [plans/pdf]"
- "process drawing numbers in [plans/pdf]"
- "generate drawing list from [plans/pdf]"
- "populate drawings to DATA spreadsheet"
- "add drawings to [project] DATA file"

**Semantic matching:** Any phrase meaning "extract/get/pull/parse/list/read drawings/drawing numbers/drawing register from PDF/plans" will trigger this skill.

## Prerequisites

- Python 3.8+
- PyMuPDF (`pip install pymupdf`)
- openpyxl (`pip install openpyxl`) - for DATA spreadsheet population
- PDF file containing architectural/structural drawings with text layer (not scanned images)
- Scripts location: `/workspace/projects/grey-docs/00-engine/`

## Quick Usage

### Mode 1: Text Register Only

```bash
cd /workspace/projects/grey-docs

# Basic usage (output in same directory as PDF)
python3 00-engine/extract_drawing_register.py "path/to/plans.pdf"

# Specify output location
python3 00-engine/extract_drawing_register.py "plans.pdf" --output "register.txt"

# Verbose mode (prints to console)
python3 00-engine/extract_drawing_register.py "plans.pdf" -v
```

### Mode 2: Populate DATA Spreadsheet

```bash
cd /workspace/projects/grey-docs

# Populate drawings for AO 1
python3 00-engine/populate_drawings.py "plans.pdf" "DATA - Project.xlsx" 1

# Dry run (preview changes without saving)
python3 00-engine/populate_drawings.py "plans.pdf" "DATA - Project.xlsx" 1 --dry-run

# Learn patterns for new consultants (first time only)
python3 00-engine/populate_drawings.py "plans.pdf" --learn
```

## Workflow A: Text Register Only

### Step 1: Locate the Plans PDF

Ask Steve for the PDF location or search common locations:

```bash
# Search in current project
find /clients/current/ -name "*plans*.pdf" -o -name "*notice*.pdf"

# Search in grey-docs
find /workspace/projects/grey-docs -name "*.pdf" -type f
```

### Step 2: Run the Extraction Script

```bash
cd /workspace/projects/grey-docs

# Run extraction
python3 00-engine/extract_drawing_register.py "path/to/plans.pdf"
```

The script will:
1. Extract all text from the PDF
2. Identify drawing numbers using regex patterns
3. Match drawing titles (plans, elevations, sections)
4. Find revisions (A, B, C, etc.)
5. Extract scales (1:50, 1:100, etc.)
6. Recognize known consultants by website/email
7. Generate structured text register

### Step 3: Review Output

The output file will be in the same directory as the input PDF with `-drawing-register.txt` suffix.

Example: `Notice Plans.pdf` → `Notice Plans-drawing-register.txt`

### Step 4: Report to Steve

Provide:
- ✅ Number of drawings found
- ✅ Consultants identified
- ✅ Output file location
- ⚠️ Any issues (no text layer, unsupported formats, etc.)

---

## Workflow B: Populate DATA Spreadsheet

### Step 1: Locate Project Files

Find both the PDF plans and the DATA spreadsheet:

```bash
# Find project directory
find /clients/current/ -name "*[address/name]*" -type d

# Verify files exist
ls "/clients/current/[PROJECT]/00.0 - DATA"*
ls "/clients/current/[PROJECT]/00-plans/"*.pdf
```

### Step 2: Check for Known Consultants

The script uses **saved consultant patterns** for deterministic processing. Check if consultants are known:

```bash
cd /workspace/projects/grey-docs

# List known consultants
cat 00-engine/consultant-patterns.json | grep -E "name|website"
```

**Known consultants:**
- Joe Wright Architects (`joewrightarchitects.co.uk`)
- Croft Structural Engineers Ltd (`croftse.co.uk`)

### Step 3: Learn New Consultant Patterns (First Time Only)

If PDF contains consultants not in the patterns file:

```bash
# Scan PDF and create pattern templates
python3 00-engine/populate_drawings.py "path/to/plans.pdf" --learn
```

This creates template entries in `00-engine/consultant-patterns.json` that need **manual review**:

1. ✅ Verify `name` is correct
2. ✅ Set `drawing_number_pattern` regex from sample drawings
3. ✅ Configure `categorization_rules` (Existing/Demolition/Proposed)
4. ✅ Save the file

**Example pattern entry:**
```json
{
  "joewrightarchitects.co.uk": {
    "name": "Joe Wright Architects",
    "type": "Architect",
    "drawing_number_pattern": "(\\d{4}-\\d{2}_\\d{2}_\\d{3})",
    "drawing_number_example": "2403-01_01_000",
    "categorization_rules": [
      {"field": "drawing_no", "pattern": "_01_", "category": "Demolition"},
      {"field": "drawing_no", "pattern": "_02_", "category": "Proposed"},
      {"field": "drawing_no", "pattern": "_00_", "category": "Existing"}
    ]
  }
}
```

### Step 4: Copy DATA File to Working Directory

```bash
# Copy DATA spreadsheet (only one DATA file should exist in grey-docs at a time)
rm -f /workspace/projects/grey-docs/00.0\ -\ DATA*
cp "/clients/current/[PROJECT]/00.0 - DATA - [details].xlsx" /workspace/projects/grey-docs/
```

### Step 5: Run Population Script

```bash
cd /workspace/projects/grey-docs

# Dry run first (preview changes)
python3 00-engine/populate_drawings.py "plans.pdf" "00.0 - DATA - [details].xlsx" 1 --dry-run

# If preview looks good, populate for real
python3 00-engine/populate_drawings.py "plans.pdf" "00.0 - DATA - [details].xlsx" 1
```

**Parameters:**
- `plans.pdf` - Path to PDF plans
- `00.0 - DATA - [details].xlsx` - DATA spreadsheet filename
- `1` - AO number (1-20)
- `--dry-run` - Optional: preview without saving

### Step 6: Verify Population

The script populates the **Form sheet** at these locations:

**For AO 1:**
- E324: Architect name
- E325: Engineer name
- B328:B352: Architect Existing drawings (25 slots)
- D328:D352: Architect Demolition drawings (25 slots)
- F328:F352: Architect Proposed drawings (25 slots)
- H328:H352: Engineer drawings (25 slots)

**Column offset:** +8 columns per AO (AO2 starts at column J, AO3 at R, etc.)

### Step 7: Copy Back to Project

```bash
# Copy updated DATA file back to project
cp "00.0 - DATA - [details].xlsx" "/clients/current/[PROJECT]/"
```

### Step 8: Report to Steve

Provide:
- ✅ Consultants detected (Architect/Engineer names)
- ✅ Drawings populated per category (Existing/Demolition/Proposed/Structural)
- ✅ AO number populated
- ⚠️ Any new consultants requiring pattern review

## Output Format

```
DRAWING REGISTER
================================================================================
Project: 1 & 2 St. Luke's Street, London, SW3 3RS
Works: Party Wall Matters
Source: Notice Plans.pdf
Extracted: 2026-02-26 21:45:00

================================================================================
CONSULTANTS
================================================================================

ARCHITECT
  Name:     Joe Wright Architects
  Website:  www.joewrightarchitects.co.uk
  Email:    info@joewrightarchitects.co.uk

STRUCTURAL ENGINEER
  Name:     Croft Structural Engineers Ltd
  Website:  www.croftse.co.uk

================================================================================
DRAWINGS
================================================================================

Drawing No.            | Title                          | Rev  | Scale
-----------------------+--------------------------------+------+-------------

DEMOLITION DRAWINGS
2403-01_01_000         | Plot Plan                      | A    | 1:100 @ A3
2403-01_01_001         | Cellar Plan                    | B    | 1:50 @ A3
2403-01_01_002         | Ground Floor Plan              | B    | 1:50 @ A3

PROPOSED DRAWINGS
2403-01_02_000         | Proposed Cellar Plan           | C    | 1:50 @ A3
2403-01_02_001         | Proposed Ground Floor Plan     | C    | 1:50 @ A3

STRUCTURAL DRAWINGS
SL-01                  | Foundation Plan                | A    | 1:50
SL-02                  | Structural Details             | B    | Various
```

## How It Works

The script uses **deterministic regex patterns** (no AI inference) to extract:

1. **Consultant info** - Websites, emails, known practice names
2. **Drawing numbers** - Multiple formats supported (see below)
3. **Drawing titles** - Plans, elevations, sections, details
4. **Revisions** - Letters A-Z, typically from title block
5. **Scales** - 1:50, 1:100, etc.
6. **Drawing type** - Demolition, Proposed, Existing, Structural

### Supported Drawing Number Formats

| Format | Example | Used By |
|--------|---------|---------|
| `YYYY-PP_TT_NNN` | 2403-01_01_000 | Joe Wright Architects |
| `SL-NN` | SL-10 | Croft Structural |
| `ABC-NNN` | PWD-001 | Generic project prefix |
| `A-NNN` | A-001 | Simple format |
| `NNN` | 001, 002 | Minimal format |

## Configuration

### Consultant Patterns File

**Location:** `/workspace/projects/grey-docs/00-engine/consultant-patterns.json`

This JSON file stores learned naming conventions for consultants. Each entry contains:

```json
{
  "domain-key.co.uk": {
    "name": "Consultant Name",
    "type": "Architect" | "Engineer",
    "website": "www.domain.co.uk",
    "drawing_number_pattern": "regex pattern with capturing group",
    "drawing_number_example": "Sample drawing number",
    "categorization_rules": [
      {
        "field": "drawing_no" | "title" | "text",
        "pattern": "regex to match",
        "category": "Existing" | "Demolition" | "Proposed" | "Structural"
      }
    ],
    "notes": "Human-readable notes about this pattern"
  }
}
```

### Adding New Consultants

**Method 1: Automatic Learning (Recommended)**
```bash
python3 00-engine/populate_drawings.py "plans.pdf" --learn
```

Then edit `/workspace/projects/grey-docs/00-engine/consultant-patterns.json` to review and refine the generated template.

**Method 2: Manual Addition**

Add entry directly to `consultant-patterns.json`:

```json
{
  "newarchitect.co.uk": {
    "name": "New Architect Ltd",
    "type": "Architect",
    "website": "www.newarchitect.co.uk",
    "drawing_number_pattern": "([A-Z]{3}-\\d{3})",
    "drawing_number_example": "ABC-001",
    "categorization_rules": [
      {"field": "title", "pattern": "demolition", "category": "Demolition"},
      {"field": "title", "pattern": "proposed", "category": "Proposed"},
      {"field": "title", "pattern": "existing|survey", "category": "Existing"}
    ],
    "notes": "Standard prefix format ABC-NNN",
    "last_updated": "2026-02-26"
  }
}
```

### Categorization Rules

Rules are applied **in order** - first match wins.

**Available fields:**
- `drawing_no` - Match against extracted drawing number
- `title` - Match against extracted title
- `text` - Match against full page text

**Available categories:**
- `Existing` - Existing condition/survey drawings
- `Demolition` - Demolition plans
- `Proposed` - Proposed works drawings
- `Structural` - All structural engineer drawings

**Example rules:**
```json
[
  {"field": "drawing_no", "pattern": "_01_", "category": "Demolition"},
  {"field": "drawing_no", "pattern": "_02_", "category": "Proposed"},
  {"field": "title", "pattern": "existing|survey", "category": "Existing"}
]
```

## DATA Spreadsheet Cell Mappings

The `populate_drawings.py` script writes to the **Form sheet** of the DATA spreadsheet.

### Cell Locations by AO

| Field | AO 1 | AO 2 | AO 3 | AO 4 |
|-------|------|------|------|------|
| **Architect Name** | E324 | M324 | U324 | AC324 |
| **Engineer Name** | E325 | M325 | U325 | AC325 |
| **Architect Existing** | B328:B352 | J328:J352 | R328:R352 | Z328:Z352 |
| **Architect Demolition** | D328:D352 | L328:L352 | T328:T352 | AB328:AB352 |
| **Architect Proposed** | F328:F352 | N328:N352 | V328:V352 | AD328:AD352 |
| **Engineer** | H328:H352 | P328:P352 | X328:X352 | AF328:AF352 |

### Pattern
- **Column offset:** +8 columns per AO
- **Drawing rows:** 25 slots per category (rows 328-352)
- **Names:** Row 324 (Architect), Row 325 (Engineer)

### Column Reference
```
AO 1: B, D, E, F, H (names at E)
AO 2: J, L, M, N, P (names at M)
AO 3: R, T, U, V, X (names at U)
AO 4: Z, AB, AC, AD, AF (names at AC)
```

---

## Troubleshooting

### No drawings detected

**Cause:** Drawing numbers use unsupported format or PDF has no text layer.

**Solution:**
1. Check PDF has extractable text:
   ```bash
   pdftotext file.pdf - | head -50
   ```
2. If no text output, PDF needs OCR first
3. If text exists, run `--learn` mode or add custom pattern to consultant-patterns.json

### "No saved pattern for: [website]"

**Cause:** New consultant not in patterns database.

**Solution:**
```bash
# Learn patterns from this PDF
python3 00-engine/populate_drawings.py "plans.pdf" --learn

# Review and edit the generated pattern
nano 00-engine/consultant-patterns.json

# Then run populate again
python3 00-engine/populate_drawings.py "plans.pdf" "DATA.xlsx" 1
```

### Wrong categorization

**Cause:** Categorization rules don't match this consultant's format.

**Solution:** Edit `categorization_rules` in consultant-patterns.json:
```json
"categorization_rules": [
  {"field": "drawing_no", "pattern": "YOUR_PATTERN", "category": "Demolition"},
  {"field": "title", "pattern": "proposed", "category": "Proposed"}
]
```

### Missing drawing numbers

**Cause:** Drawing number pattern regex doesn't match this format.

**Solution:** Update `drawing_number_pattern` in consultant-patterns.json with correct regex.

### Wrong revisions detected

**Cause:** Title block format differs from expected patterns.

**Solution:** Script looks for:
- Date (DD.MM.YY) followed by single letter on next line
- "Rev:" or "Revision:" label followed by letter

Check PDF title block format.

### Import errors

**Cause:** Missing Python dependencies.

**Solution:**
```bash
pip3 install pymupdf openpyxl
```

### DATA file already exists error

**Cause:** Multiple DATA files in working directory.

**Solution:**
```bash
# Remove old DATA files first
rm -f /workspace/projects/grey-docs/00.0\ -\ DATA*

# Then copy the correct one
cp "[PROJECT_DATA_FILE]" /workspace/projects/grey-docs/
```

## Limitations

- ✅ **Text-based PDFs only** - Scanned image PDFs need OCR first
- ✅ **Title block dependent** - Best results with consistent title blocks
- ✅ **One PDF at a time** - Run script separately for each PDF file
- ✅ **Pattern matching** - May miss unconventional drawing numbering systems

## Example Sessions

### Example 1: Text Register Only

**Steve:** "Extract drawing numbers from the St Luke's plans"

**Agent:**
1. Searches for PDF: `/clients/current/1 & 2 St Lukes Street/00-plans/Notice Plans.pdf`
2. Runs extraction: `python3 00-engine/extract_drawing_register.py "Notice Plans.pdf"`
3. Reports:
   - ✅ Found 12 drawings
   - ✅ Identified Joe Wright Architects + Croft Structural
   - ✅ Output: `Notice Plans-drawing-register.txt`

### Example 2: Populate DATA Spreadsheet

**Steve:** "Populate drawings for St Luke's AO 1"

**Agent:**
1. Finds project: `/clients/current/1 & 2 St Lukes Street/`
2. Locates files:
   - PDF: `00-plans/Notice Plans.pdf`
   - DATA: `00.0 - DATA - St Lukes.xlsx`
3. Checks consultant patterns (Joe Wright & Croft already known)
4. Copies DATA file to working directory
5. Runs: `python3 00-engine/populate_drawings.py "Notice Plans.pdf" "00.0 - DATA - St Lukes.xlsx" 1 --dry-run`
6. Reviews preview, then runs without --dry-run
7. Reports:
   - ✅ Architect: Joe Wright Architects
   - ✅ Engineer: Croft Structural Engineers Ltd
   - ✅ Existing: 3 drawings
   - ✅ Demolition: 4 drawings
   - ✅ Proposed: 4 drawings
   - ✅ Structural: 2 drawings
   - ✅ Populated to AO 1 (columns B, D, F, H)
8. Copies updated DATA file back to project

### Example 3: New Consultant (Learning Required)

**Steve:** "Populate drawings for new project with ABC Architects"

**Agent:**
1. Locates PDF with ABC Architects plans
2. Runs: `python3 00-engine/populate_drawings.py "plans.pdf" --learn`
3. Script creates template in consultant-patterns.json
4. Reviews template and identifies:
   - Drawing number format: ABC-NNN
   - Pattern: `(ABC-\d{3})`
   - Categorization: Based on title keywords
5. Edits consultant-patterns.json with correct rules
6. Re-runs populate command
7. Reports successful population with new consultant

## Files

| File | Purpose |
|------|---------|
| `00-engine/extract_drawing_register.py` | Text register extraction (standalone) |
| `00-engine/populate_drawings.py` | DATA spreadsheet population script |
| `00-engine/consultant-patterns.json` | Saved consultant naming conventions |
| `*-drawing-register.txt` | Output text register files |
| `DRAWING-EXTRACTION-WORKFLOW.md` | Extended workflow documentation |
| `DRAWING-REGISTER-WORKFLOW.md` | Text register workflow documentation |
| `skill-extract-drawing-numbers.md` | This skill documentation |

## Dependencies

```bash
# Install required Python packages
pip3 install pymupdf openpyxl

# Verify installation
python3 -c "import fitz; print('PyMuPDF:', fitz.__version__)"
python3 -c "import openpyxl; print('openpyxl:', openpyxl.__version__)"
```

## Key Principles

### Deterministic Processing

The populate script uses **explicit rules only** - no AI inference after initial pattern learning:

1. **One-time learning:** When encountering a new consultant, run `--learn` mode to create pattern template
2. **Manual review:** Human reviews and refines the auto-generated pattern
3. **Deterministic execution:** All subsequent processing uses saved patterns only

### Inference vs. Determinism

- ✅ **Deterministic:** Pattern matching with saved regex rules
- ✅ **Deterministic:** Category assignment via explicit rules
- ❌ **No inference:** Script never guesses drawing categories
- ❌ **No inference:** Script never invents drawing number patterns

This ensures **reproducible, predictable results** every time.

## Integration with Notices Workflow

This skill complements the `notices` skill:

1. **Before notices:** Extract drawing register to verify plans are complete
2. **With notices:** Populate DATA spreadsheet with drawing numbers
3. **Quality check:** Ensure all referenced drawings exist in plans PDF
4. **Documentation:** Include drawing list in party wall documentation

## API / Commands

No external APIs required. Pure Python regex-based extraction with openpyxl for Excel manipulation.
