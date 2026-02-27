---
name: awards
description: Generate Party Wall Award documents (D1/D2) for Grey & Associates projects
command: awards
---

# Awards Skill

Generate Party Wall Award documents (D1 or D2) for Grey & Associates projects.

## Trigger Phrases

This skill activates when you ask for:
- "D1 Award for [project]"
- "D2 Award for [project]"
- "generate D1 Award for [AO/project]"
- "produce D2 Award for [AO/project]"
- "create award for [project]"
- "D1 Addendum Award for [project]"
- "D2 Addendum Award for [project]"
- "addendum award for [project]"

**Semantic matching:** Any phrase meaning "generate/produce/create D1/D2 Award/Addendum Award" will trigger this skill.

## Prerequisites

- **data-spreadsheet skill** — Read this first to understand DATA spreadsheet structure
- **extract-drawing-numbers skill** — For populating drawing numbers in DATA spreadsheet
- **Python deps** installed (openpyxl, docxtpl, pick, requests)
- **docs.py** at `/workspace/projects/grey-docs/00-engine/docs.py`
- **Templates** at `/workspace/projects/grey-docs/00-engine/templates/`

## Award Types

| Type | Template | When to Use |
|------|----------|-------------|
| **D1 Award** | `12.1 - Award D1.docx` | Standard dispute resolution award |
| **D2 Award** | `12.1 - Award D2.docx` | Alternative award type |
| **D1 Addendum** | `15.1 - Addendum Award D1.docx` | Addendum to existing D1 Award |
| **D2 Addendum** | `15.1 - Addendum Award D2.docx` | Addendum to existing D2 Award |

**Output Format:** Awards are saved as `.docx` files only (no PDF generation).

---

## Workflow

### Step 1: Identify the Project

Steve will provide either a **road name**, **Building Owner name**, or **AO number**. Search for the project:

```bash
# Search by road name
find /clients/current/ -maxdepth 1 -iname "*[road_name]*" -type d

# Search by BO name
find /clients/current/ -maxdepth 1 -iname "*[bo_name]*" -type d
```

Confirm with Steve if multiple matches are found.

---

### Step 2: Copy the DATA Spreadsheet

Find and copy the `*DATA*` file from the project directory to the working directory:

```bash
# Find DATA file
ls /clients/current/<project-dir>/00.0\ -\ DATA*

# Copy to working directory (overwrite any existing DATA file first)
rm -f /workspace/projects/grey-docs/00.0\ -\ DATA*
cp "/clients/current/<project-dir>/00.0 - DATA - ....xlsx" /workspace/projects/grey-docs/
```

**Important:** Only one DATA file should exist in the grey-docs directory at a time.

---

### Step 3: Identify Active AOs

Read the DATA spreadsheet and identify which AOs are active. An AO is active if cell `A2` or `B2` on its AO sheet has non-empty text.

```bash
# If Steve specified an AO number, use that
# Otherwise, scan all AO sheets (1-20) to find active AOs
```

**Ask Steve if needed:**
- "Which AO should I generate the award for?"
- If generating for multiple AOs, confirm the list

---

### Step 4: Verify Drawing Numbers are Populated

**Critical:** Awards require drawing numbers to be populated in the DATA spreadsheet.

**Check Form sheet drawing cells** for the specified AO:

For **AO 1**, check these ranges:
- **Architect Existing:** B328:B352 (25 slots)
- **Architect Demolition:** D328:D352 (25 slots)
- **Architect Proposed:** F328:F352 (25 slots)
- **Engineer:** H328:H352 (25 slots)

**Column offset:** +8 columns per AO (AO2 at J, AO3 at R, etc.)

**If drawing numbers are MISSING:**

1. **Alert Steve:**
   ```
   ⚠️ Drawing numbers are not populated for AO [n] in the DATA spreadsheet.

   Would you like me to:
   1. Extract and populate drawings from plans PDF (provide PDF path)
   2. Continue anyway (award will have empty drawing lists)
   3. Abort
   ```

2. **If Steve provides PDF path:**
   - Use `extract-drawing-numbers` skill workflow
   - Run: `python3 00-engine/populate_drawings.py "plans.pdf" "DATA.xlsx" <ao_number>`
   - Verify population succeeded
   - Copy updated DATA file back to project directory
   - Continue with award generation

3. **If Steve says continue anyway:**
   - Proceed with warning that drawing lists will be empty

4. **If Steve says abort:**
   - Stop workflow

---

### Step 5: Determine Award Type

Based on Steve's trigger phrase:

| Trigger Contains | Award Type | Template |
|------------------|------------|----------|
| "D1 Award" (no "addendum") | D1 Award | `12.1 - Award D1` |
| "D2 Award" (no "addendum") | D2 Award | `12.1 - Award D2` |
| "D1 Addendum" OR "Addendum D1" | D1 Addendum | `15.1 - Addendum Award D1` |
| "D2 Addendum" OR "Addendum D2" | D2 Addendum | `15.1 - Addendum Award D2` |

**If unclear, ask Steve:**
```
Which award type should I generate?
1. D1 Award (12.1 - Award D1)
2. D2 Award (12.1 - Award D2)
3. D1 Addendum Award (15.1 - Addendum Award D1)
4. D2 Addendum Award (15.1 - Addendum Award D2)
```

---

### Step 6: Generate Award Document

Run docs.py with the appropriate document name:

```bash
cd /workspace/projects/grey-docs/00-engine
python3 docs.py --ao <ao_number> --doc "<award_template_name>" --docx --no-pdf
```

**Document names:**
- `12.1 - Award D1` — D1 Award
- `12.1 - Award D2` — D2 Award
- `15.1 - Addendum Award D1` — D1 Addendum Award
- `15.1 - Addendum Award D2` — D2 Addendum Award

**Important:** Always use `--docx --no-pdf` flags. This generates .docx only (no PDF).

**Output location:** `/workspace/projects/grey-docs/AO <n>/`

---

### Step 7: Verify Award Data

Awards pull data from multiple sections of the DATA spreadsheet. Key fields include:

**From AO Sheet (row 2):**
- AO names and addresses
- Property details
- Surveyor information
- Award sections (cell BQ2)

**From Form Sheet:**
- Building Owner (BO) details (rows 3, 11)
- Notice data (Section 1/2/6 details)
- Drawing numbers (rows 328-352, offset per AO)
- Correspondence addresses (rows 40, 44, 48, 52, 56, offset per AO)

**Common validation errors:**
- Empty description fields (s1_detail, s2_detail, s6_detail)
- Missing drawing numbers
- Stale Excel formula cache

**If docs.py shows warnings:**
```
⚠️  WARNING: Key values are EMPTY!
  - s1_detail_1 is empty
  - Drawing lists are empty

This usually means the Excel file formula cache is stale.
FIX: Open the DATA file in Excel/LibreOffice, then SAVE it
```

**Action:**
1. Report warning to Steve
2. Ask if they want to continue anyway or fix the DATA file first

---

### Step 8: Report Results

After generation, report to Steve:

```
✅ Award generated for [project name]:

AO [n] — [AO Name] ([property address])
  • Award Type: [D1/D2/D1 Addendum/D2 Addendum]
  • Template: [12.1 - Award D1/D2 / 15.1 - Addendum Award D1/D2]
  • Output: /workspace/projects/grey-docs/AO [n]/[filename].docx
  • Drawing Numbers: [✅ Populated / ⚠️ Empty]

📄 Award saved as .docx for manual editing.
```

**Example:**
```
✅ Award generated for 1 & 2 St. Luke's Street:

AO 1 — Pedro Trebbau (1 St. Luke's Street, London, SW3 3RS)
  • Award Type: D1 Award
  • Template: 12.1 - Award D1
  • Output: /workspace/projects/grey-docs/AO 1/12.1 - Award D1 (AO 1).docx
  • Drawing Numbers: ✅ Populated (12 Existing, 8 Demolition, 10 Proposed, 4 Structural)

📄 Award saved as .docx for manual editing.
```

---

### Step 9: Copy Award to Project Directory (Optional)

If Steve requests, copy the generated award back to the project directory:

```bash
cp "/workspace/projects/grey-docs/AO 1/12.1 - Award D1 (AO 1).docx" \
   "/clients/current/[PROJECT]/AO 1/"
```

---

## Important Notes

### Awards vs Notices

**Similarities:**
- Both use DATA spreadsheet (Form sheet + AO sheets)
- Both require active AO validation
- Both use docs.py for generation

**Differences:**
- **Awards:** Saved as .docx only (manual editing required)
- **Notices:** Converted to PDF via Gotenberg
- **Awards:** Require drawing numbers to be populated
- **Notices:** Drawing numbers optional

### Drawing Numbers Requirement

Awards **always** need drawing numbers populated in DATA spreadsheet because:
- Awards reference specific drawings in dispute
- Drawing lists are included in award body
- Legal requirement for clarity

**Workflow integration:**
1. Check if drawings populated
2. If missing, use `extract-drawing-numbers` skill
3. Populate DATA spreadsheet
4. Then generate award

### Data Validation

Awards pull from the same Form sheet data as notices:
- Section 1/2/6 details (description of works)
- Notice requirements (Yes/No in G72, G81, G101)
- Building Owner and Adjoining Owner details

**Critical cells for awards:**
- Award sections reference: BQ2 on AO sheet
- Drawing lists: Form sheet rows 328-352 (offset +8 per AO)
- All notice description fields

### Output Format

**Output Files:**
- Awards generate **.docx files only** (no PDF)
- The `--no-pdf` flag prevents PDF generation for Awards
- Steve makes case-specific modifications to the .docx before finalizing
- Notice documents still generate PDFs as normal

---

## Column Offset Reference

Same as notices workflow:

| AO | Base Column | Drawing Columns | Calculation |
|----|-------------|-----------------|-------------|
| AO 1 | B (2) | B, D, E, F, H | base |
| AO 2 | J (10) | J, L, M, N, P | +8 |
| AO 3 | R (18) | R, T, U, V, X | +16 |
| AO n | — | — | base + (n-1)×8 |

**Drawing columns (AO 1):**
- B328:B352 — Architect Existing
- D328:D352 — Architect Demolition
- E324 — Architect Name
- E325 — Engineer Name
- F328:F352 — Architect Proposed
- H328:H352 — Engineer

---

## Integration with Other Skills

### data-spreadsheet
Required to understand DATA file structure, cell locations, and AO sheet organization.

### extract-drawing-numbers
Used when drawings are missing from DATA spreadsheet:
1. Trigger: Award needs drawings but none found
2. Steve provides plans PDF path
3. Run populate_drawings.py for the AO
4. Verify population succeeded
5. Continue award generation

### notices
Shares workflow patterns:
1. Project identification
2. DATA file copying
3. AO validation
4. docs.py execution
5. Result reporting

---

## Troubleshooting

### "Key values are EMPTY" Warning

**Cause:** Excel formula cache is stale in AO sheets.

**Solution:**
1. Open DATA file in Excel/LibreOffice
2. Save the file (even without changes)
3. This updates the formula cache
4. Re-run docs.py

### No Drawing Numbers Found

**Cause:** Form sheet drawing cells are empty for this AO.

**Solution:**
1. Ask Steve for plans PDF path
2. Run: `python3 00-engine/populate_drawings.py "plans.pdf" "DATA.xlsx" <ao>`
3. Verify population succeeded
4. Copy updated DATA back to project
5. Re-generate award

### Template Not Found

**Cause:** Template file missing or incorrect name.

**Solution:**
1. Verify templates exist:
   ```bash
   ls /workspace/projects/grey-docs/00-engine/templates/12.1*
   ls /workspace/projects/grey-docs/00-engine/templates/15.1*
   ```
2. Check exact template name in docs.py output
3. Use exact name with docs.py (case-sensitive)

### Multiple AOs Need Awards

**Scenario:** Project has 3 active AOs, all need awards.

**Solution:**
1. Generate for each AO separately:
   ```bash
   python3 docs.py --ao 1 --doc "12.1 - Award D1" --docx --no-pdf
   python3 docs.py --ao 2 --doc "12.1 - Award D1" --docx --no-pdf
   python3 docs.py --ao 3 --doc "12.1 - Award D1" --docx --no-pdf
   ```
2. Report results for all AOs together

---

## Example Sessions

### Example 1: D1 Award with Drawings Present

**Steve:** "Generate D1 Award for St Luke's AO 1"

**Agent:**
1. Finds project: `/clients/current/1 & 2 St Lukes Street/`
2. Copies DATA file to working directory
3. Checks AO 1 is active: ✅
4. Checks drawing numbers in Form sheet B328:H352: ✅ Populated
5. Runs: `python3 00-engine/docs.py --ao 1 --doc "12.1 - Award D1" --docx --no-pdf`
6. Reports:
   ```
   ✅ Award generated for 1 & 2 St. Luke's Street:

   AO 1 — Pedro Trebbau (1 St. Luke's Street, London, SW3 3RS)
     • Award Type: D1 Award
     • Template: 12.1 - Award D1
     • Output: /workspace/projects/grey-docs/AO 1/12.1 - Award D1 (AO 1).docx
     • Drawing Numbers: ✅ Populated

   📄 Award saved as .docx for manual editing.
   ```

### Example 2: D2 Award with Missing Drawings

**Steve:** "Create D2 Award for Lancaster Road AO 2"

**Agent:**
1. Finds project: `/clients/current/15 Lancaster Road/`
2. Copies DATA file
3. Checks AO 2 is active: ✅
4. Checks drawing numbers in Form sheet J328:P352: ❌ Empty
5. Alerts Steve:
   ```
   ⚠️ Drawing numbers are not populated for AO 2 in the DATA spreadsheet.

   Would you like me to:
   1. Extract and populate drawings from plans PDF (provide PDF path)
   2. Continue anyway (award will have empty drawing lists)
   3. Abort
   ```
6. Steve provides: `/clients/current/15 Lancaster Road/00-plans/Notice Plans.pdf`
7. Runs: `python3 00-engine/populate_drawings.py "Notice Plans.pdf" "DATA.xlsx" 2`
8. Reports: ✅ Populated 8 Existing, 6 Demolition, 7 Proposed, 3 Structural
9. Copies updated DATA back to project
10. Generates award: `python3 00-engine/docs.py --ao 2 --doc "12.1 - Award D2" --docx --no-pdf`
11. Reports success with drawing counts

### Example 3: D1 Addendum Award

**Steve:** "Generate D1 Addendum Award for Hardy project AO 1"

**Agent:**
1. Searches by BO name "Hardy"
2. Finds project, copies DATA file
3. Validates AO 1 active: ✅
4. Checks drawings populated: ✅
5. Identifies award type: **D1 Addendum** (from "Addendum" keyword)
6. Runs: `python3 00-engine/docs.py --ao 1 --doc "15.1 - Addendum Award D1" --docx --no-pdf`
7. Reports:
   ```
   ✅ Award generated for [Hardy Project]:

   AO 1 — [AO Name] ([property])
     • Award Type: D1 Addendum Award
     • Template: 15.1 - Addendum Award D1
     • Output: /workspace/projects/grey-docs/AO 1/15.1 - Addendum Award D1 (AO 1).docx
     • Drawing Numbers: ✅ Populated
   ```

---

## Files

| File | Purpose |
|------|---------|
| `00-engine/docs.py` | Award generation script |
| `00-engine/templates/12.1 - Award D1.docx` | D1 Award template |
| `00-engine/templates/12.1 - Award D2.docx` | D2 Award template |
| `00-engine/templates/15.1 - Addendum Award D1.docx` | D1 Addendum template |
| `00-engine/templates/15.1 - Addendum Award D2.docx` | D2 Addendum template |
| `AO <n>/12.1 - Award D1 (AO <n>).docx` | Generated D1 Award output |
| `AO <n>/12.1 - Award D2 (AO <n>).docx` | Generated D2 Award output |
| `skill-awards.md` | This skill documentation |

---

## Key Rules

1. **Always check drawings first** — Awards require drawing numbers
2. **Use extract-drawing-numbers skill** — If drawings missing, populate from PDF
3. **Output format** — .docx only (use `--no-pdf` flag to skip PDF generation)
4. **Validate AO is active** — Check A2/B2 on AO sheet
5. **Use --docx --no-pdf flags** — Essential for docs.py to generate .docx only
6. **One DATA file at a time** — Remove old DATA before copying new
7. **Report drawing counts** — Include in success message
8. **Copy DATA back** — If drawings were populated, update project file

---

## Dependencies

- Python 3.8+
- openpyxl (`pip install openpyxl`)
- docxtpl (`pip install docxtpl`)
- pick (`pip install pick`)
- requests (`pip install requests`)
