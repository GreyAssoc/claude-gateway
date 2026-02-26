---
name: notices
description: Generate party wall notice documents for Grey & Associates projects
command: notices
---

# Notices Skill

Generate party wall notice documents (.docx & .pdf) for Grey & Associates projects.

## Trigger Phrases
- "produce notices for [project]"
- "generate notices for [road name / BO name]"
- "notices for [project]"

## Prerequisites
- **data-spreadsheet skill** — read this first to understand DATA spreadsheet structure
- **Python deps** installed via `pip --user` (openpyxl, docxtpl, pick, requests)
- **Gotenberg** at `http://172.17.0.1:3000` for PDF conversion
- **docs.py** at `/workspace/projects/grey-docs/00-engine/docs.py`
- **Templates** at `/workspace/projects/grey-docs/00-engine/templates/`

## Workflow

### Step 1: Identify the Project
Steve will provide either a **road name** or **Building Owner name**. Search for the project:

```bash
# Search by road name
find /clients/current/ -maxdepth 1 -iname "*lancaster*" -type d

# Search by BO name
find /clients/current/ -maxdepth 1 -iname "*hardy*" -type d
```

Confirm with Steve if multiple matches are found.

### Step 2: Copy the DATA Spreadsheet
Find and copy the `*DATA*` file from the project directory to the working directory:

```bash
# Find DATA file
ls /clients/current/<project-dir>/00.0\ -\ DATA*

# Copy to working directory (overwrite any existing DATA file first)
rm -f /workspace/projects/grey-docs/00.0\ -\ DATA*
cp "/clients/current/<project-dir>/00.0 - DATA - ....xlsx" /workspace/projects/grey-docs/
```

**Important:** Only one DATA file should exist in the grey-docs-generation directory at a time.

### Step 3: Read & Validate the DATA Spreadsheet
Load the `data-spreadsheet` skill for reference. Then read the Form sheet and check:

#### Identify Active AOs
Scan AO 1–20 sheets. An AO is active if cell `A2` or `B2` on its AO sheet has non-empty text.

#### For Each Active AO, Validate Notice Data
The column offset per AO is `(ao_number - 1) × 8`. All cell references below are for **AO 1** — shift right by 8 columns for each subsequent AO.

**Notice 1 (Section 1):**
| Check | Cell | Expected |
|-------|------|----------|
| Dropdown | G72 | "Yes" or "No" |
| S1 section choice | B74 | Data if Yes |
| PW/PFW/Both | F74 | Data if Yes |
| Description line 1 | B77 | Data if Yes |
| Description line 2 | B78 | Optional |

**Notice 2 (Section 2):**
| Check | Cell | Expected |
|-------|------|----------|
| Dropdown | G81 | "Yes" or "No" |
| Sections reference | E82 | Data if Yes |
| Description lines | B83–B97 | Some data if Yes (up to 15 lines) |

**Notice 6 (Section 6):**
| Check | Cell | Expected |
|-------|------|----------|
| Dropdown | G101 | "Yes" or "No" |
| Underpin IS/IS NOT | E102 | Data if Yes |
| Section reference | G102 | Data if Yes |
| Description lines | B103–B109 | Some data if Yes |

#### Validation Rules — Flag as Potential Data Entry Error:
1. **Dropdown = "Yes" but no corresponding data** → ⚠️ Flag: "Notice [1/2/6] for AO [n] is marked Yes but has no description data"
2. **Dropdown ≠ "Yes" but data exists in cells** → ⚠️ Flag: "Notice [1/2/6] for AO [n] has description data but is not marked Yes"

If any errors are flagged, **report them to Steve and wait for instruction** before proceeding.

### Step 4: Determine Which Notices to Generate
For each active AO (or the specific AO Steve requested):

1. Read the Yes/No values from G72, G81, G101 (offset per AO)
2. Determine the notice combination:
   - S1 only → `(s1)` template
   - S2 only → `(s2)` template  
   - S6 only → `(s6)` template
   - S1+S2 → `(s1, s2)` template
   - S1+S6 → `(s1, s6)` template
   - S2+S6 → `(s2, s6)` template
   - S1+S2+S6 → `(ALL)` template

3. Determine variants needed:
   - **Always** generate Prop + Prop (Email)
   - Check Form sheet correspondence address rows 40, 44, 48, 52, 56 (cols B-H, offset +8 per AO)
   - If **any** row has data → generate Corr + Corr (Email) for each populated address
   - If **none** have data → no Corr notices needed
   - **Ignore** the "selected correspondence address" in row 36 and the AO sheet K2 vs L2 comparison

4. **Multiple correspondence addresses:** docs.py automatically detects populated correspondence addresses from Form sheet rows 40, 44, 48, 52, 56 (per AO, offset +8 cols). It generates a **separate Corr notice for each address**, suffixed `(Corr 1)`, `(Corr 2)`, etc. if more than one address exists.

### Step 5: Generate Documents
Run docs.py for each AO and each required variant:

```bash
cd /workspace/projects/grey-docs/00-engine
python3 docs.py --ao <ao_number> --doc <doc_shortcut>
```

**Document shortcuts for notices:**
| Shortcut | Document |
|----------|----------|
| `notices-prop` | Notices Served - Prop (AUTO) |
| `notices-prop-email` | Notices Served - Prop (Email) (AUTO) |
| `notices-corr` | Notices Served - Corr (AUTO) |
| `notices-corr-email` | Notices Served - Corr (Email) (AUTO) |

The `(AUTO)` flag means docs.py auto-selects the correct section combination template based on Form sheet data.

### Step 6: Report Results
After generation, report to Steve:

```
✅ Notices generated for [project name]:

AO 1 — [AO Name] ([property address])
  • Notices Served (s2, s6) - Prop ✅
  • Notices Served (s2, s6) - Prop (Email) ✅
  
AO 2 — [AO Name] ([property address])  
  • Notices Served (ALL) - Prop ✅
  • Notices Served (ALL) - Prop (Email) ✅
  • Notices Served (ALL) - Corr ✅
  • Notices Served (ALL) - Corr (Email) ✅

Output: /workspace/projects/grey-docs/AO 1/
```

If all three notices apply, highlight: **Notices Served (ALL)**

## Address Formatting (docs.py)

docs.py formats correspondence addresses deterministically from Form sheet cells:

**Horizontal (single line):**
`{{Flat No}}, {{House No}} {{Road}} {{Spare}}, {{Town}}, {{County}}, {{Post Code}}`

**Vertical (multi-line, used in letter body):**
```
{{Flat No}}
{{House No}} {{Road}}
{{Spare}}
{{Town}}
{{County}}
{{Post Code}}
```

- Empty fields are skipped — next populated field moves up
- House No and Road always share a line
- If Flat No already starts with "Flat" (e.g. "Flat A"), it's used as-is; otherwise "Flat" is prepended

## Output Format

- **Default: PDF only** — .docx is generated, converted to PDF via Gotenberg, then .docx is deleted
- **To keep .docx files:** pass `--docx` flag to docs.py

```bash
cd /workspace/projects/grey-docs/00-engine
python3 docs.py --ao 1 --doc notices-prop --docx
```

## Python Dependencies

Dependencies are installed via `pip --user` (no venv needed):

```bash
# Bootstrap pip if missing
curl -sS https://bootstrap.pypa.io/get-pip.py -o /tmp/get-pip.py
python3 /tmp/get-pip.py --user --break-system-packages

# Install deps
pip install --user --break-system-packages openpyxl docxtpl pick requests
```

Run docs.py directly with `python3`:

## Column Offset Quick Reference

| AO | Notice Yes/No col | Description col | Calculation |
|----|-------------------|-----------------|-------------|
| AO 1 | G (7) | B (2) | base |
| AO 2 | O (15) | J (10) | +8 |
| AO 3 | W (23) | R (18) | +16 |
| AO n | — | — | base + (n-1)×8 |

## Important Notes
- **Read-only on /clients/current/** — always copy DATA file, never modify the original
- **One DATA file at a time** — remove old DATA files from /workspace/projects/grey-docs/ before copying new one
- **Formula cache** — docs.py reads description of works from Form sheet directly (bypasses stale AO sheet formula cache)
- **If Steve doesn't specify AO** — generate notices for ALL active AOs
- **If Steve specifies an AO** — generate notices only for that AO
