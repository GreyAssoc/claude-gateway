---
name: data-spreadsheet
description: Reference guide for Grey & Associates DATA spreadsheets
command: data
---

# DATA Spreadsheet Skill

Reference guide for understanding and working with Grey & Associates `*DATA*` spreadsheets used across all current and past party wall projects.

## Overview

Every Grey & Associates project contains one Excel spreadsheet with "DATA" in the filename (e.g. `00.0 - DATA - Hardy.Ben_Barr.Alexis - 50 Lancaster Road.xlsx`). This is the single source of truth for all project data and document generation.

## Sheet Structure

| Sheet | Purpose |
|-------|---------|
| **Form** | User data entry — all project data is entered here |
| **Data** | Intermediate calculations — takes Form data, applies formatting/calculations |
| **AO 1** – **AO 20** | Per-AO output sheets — populated from Data sheet with calculated/formatted values for each Adjoining Owner |
| **BO** | Building Owner output sheet |
| **Drawings** | Drawing register |
| **3rd Surveyors** | Third surveyor details |
| **JP & Nathan** | Internal reference |

**Data flow:** Form → Data → AO sheets (and BO sheet)

## Form Sheet Layout (A1:FE385)

### Building Owner Details (Rows 1–17, Cols B–H)

| Cell(s) | Content |
|---------|---------|
| B1 | Section header: "Building Owner - Details" |
| B2:H2 | Column headers: Title, First, Middle, Surname, _, M/F/P, _ |
| B3:H3 | BO Person 1 (e.g. Mr, Benjamin, _, Hardy, _, Plural, _) |
| B4:H4 | BO Person 2 (e.g. Ms, Alexis, Ruth, Barr) |
| G5:G6 | FH/LH label and value (e.g. "Freeholders") |
| B9 | "Property Address" label |
| B10:H10 | Address headers: Flat No, House No, Road, Spare, Town, County, Post Code |
| B11:H11 | BO property address values |
| B13 | "Correspondence Address" label |
| B14:H14 | Address headers (same as row 10) |
| B15:H15 | BO correspondence address values |

### AO1 Details (Rows 19–113, Cols B–H)

Each AO block occupies **8 columns**. AO1 = cols B–H, AO2 = cols J–P, AO3 = cols R–X, etc. (offset = 8 columns per AO).

| Row(s) | Content |
|--------|---------|
| 19 | Section header: "Adjoining Owner 1" |
| 20 | Column headers: Title, First, Middle, Surname, _, M/F/P, _ |
| 21–22 | AO person names (up to 2 people) |
| 23–24 | FH/LH (Freeholder/Leaseholder) label and value |
| 25 | Notice(s) Served date (col H) |
| 26 | Days to Add for 10 Day Letter (col H) |
| 27 | Appoint AOS By date (col H) |
| 28–29 | AO property address (Flat No, House No, Road, Spare, Town, County, Post Code) |
| 31–36 | Correspondence address selection and values |
| 38–57 | Up to 5 alternative correspondence addresses (rows 38, 42, 46, 50, 54) |
| 58 | AO email address (col E) |
| 59–64 | AO's Surveyor details (name, address) |
| 65–70 | Third Surveyor details (name, address) |
| **72** | **Notice 1 (Section 1) — "Is Notice Required:" Yes/No (col G)** |
| 73 | S1 section choice and PW/PFW/Both |
| 76 | S1 details label |
| 77–78 | S1 description of works (2 lines) |
| **81** | **Notice 2 (Section 2) — "Is Notice Required:" Yes/No (col G)** |
| 82 | S2 details header + sections reference |
| 83–97 | S2 description of works (up to 15 lines) |
| **101** | **Notice 6 (Section 6) — "Is Notice Required:" Yes/No (col G)** |
| 102 | S6 details header + underpin IS/IS NOT + section reference |
| 103–104 | S6 description of works (2 lines) |
| 116–118 | 10 Day Letter subject line and style |

### Notice Requirements — Column Mapping

The Yes/No for notice requirements is in a **specific column per AO**:

| AO | Column (letter) | Column (number) |
|----|-----------------|-----------------|
| AO 1 | G | 7 |
| AO 2 | O | 15 |
| AO 3 | W | 23 |
| AO 4 | AE | 31 |
| AO n | — | 7 + (n-1) × 8 |

Rows remain constant: **72** (S1), **81** (S2), **101** (S6).

### Description of Works — Column Mapping

The description text is in the **B-column equivalent** per AO:

| AO | Column (number) | Calculation |
|----|-----------------|-------------|
| AO 1 | 2 (B) | 2 + (1-1) × 8 |
| AO 2 | 10 (J) | 2 + (2-1) × 8 |
| AO 3 | 18 (R) | 2 + (3-1) × 8 |
| AO n | — | 2 + (n-1) × 8 |

### Conditional Consent (Rows 234–255, Cols B–H per AO)

| Row(s) | Content |
|--------|---------|
| 234 | Section header: "Adjoining Owner 1 - Conditions of Consent" |
| 235 | "Conditional Consent Conditions" label |
| 236–255 | Up to 20 consent conditions (text in col B per AO) |

Standard conditions include: mid-point cuts, steel protrusions, access notice, window protection, excavation trimming, wall protection, no storage on neighbour's land, scaffolding netting.

### Plans / Drawings (Rows 323–352, per AO)

| Cell(s) | Content |
|---------|---------|
| D324 | "Architect:" label |
| E324 | Architect name |
| D325 | "Engineer:" label |
| E325 | Engineer name |
| B326 | "Architects" label |
| H326 | "Structural" label |
| B327–H327 | Column headers: Existing, _, Demolition, _, Proposed, _, Structural |
| B328:B352 | Architect existing plans (up to 25) |
| C328:C352 | Architect demolition plans |
| D328:D352 | Architect proposed plans |
| H328:H352 | Engineer/structural plans |

**AO offset for plans:** Same +8 column rule. AO2 plans start at col J (328:352).

## AO Output Sheets (AO 1 – AO 20)

Each AO sheet contains **calculated/formatted values in row 2**, used directly by `docs.py` for template variable population. Key cells:

| Cell | Variable | Content |
|------|----------|---------|
| A2 | `ao_letter_names` | Formatted AO names for letters |
| B2 | `ao_notice_names` | Formatted AO names for notices (ALL CAPS) |
| C2 | `ao_fhlh` | Freeholders/Leaseholders |
| D2 | `ao_I_We` | I/We (pronoun) |
| E2 | `ao_my_our` | my/our |
| F2 | `ao_his_her` | his/hers/their |
| G2 | `ao_he_she` | he/she/they |
| H2 | `ao_do_does` | do/does |
| I2 | `ao_have_has` | have/has |
| J2 | `ao_him_her` | him/her/them |
| K2 | `ao_property_add_horz` | AO property address (horizontal) |
| L2 | `ao_correspond_add_horz` | AO correspondence address (horizontal) |
| M2 | `ao_property_add_vert` | AO property address (vertical/multi-line) |
| N2 | `ao_correspond_add_vert` | AO correspondence address (vertical) |
| O2 | `notice_date` | Notice served date |
| P2–AF2 | BO details | BO names, pronouns, addresses, plurals |
| AO2–AP2 | S15/S12 | Section 15/12 text and flags |
| AQ2 | `s1_pw_pfw` | S1 party wall / party fence wall |
| AR2–AS2 | `s1_detail_1/2` | S1 description of works |
| AU2 | `s2_sections` | S2 section references |
| AV2–BJ2 | `s2_detail_1–15` | S2 description of works (15 lines) |
| BL2 | `s6_underpin_is_isnot` | Underpin IS/IS NOT |
| BM2 | `s6_sections` | S6 section reference |
| BN2–BO2 | `s6_detail_1/2` | S6 description of works |
| BQ2 | `award_sections` | Award sections text |
| CU2–DN2 | `cc1–cc20` | Conditional consent conditions (up to 20) |
| CM2–CT2 | Surveyor details | AO surveyor, third surveyor names/addresses |
| DO2–DR2 | BO surveyor | BO surveyor name/address |
| EB2–EC2 | `architect_name`, `engineer_name` | Consultant names |
| ED2–EK2 | Plan references | Architect existing/demolition/proposed + engineer plans (horizontal & vertical) |

### Prop vs Corr Variant Detection

If `K2` (property address) == `L2` (correspondence address) → only **Prop** variants needed.
If they differ → both **Prop** and **Corr** variants needed.

### Active AO Detection

An AO is "active" (has a party to serve) if `A2` or `B2` on its AO sheet contains non-empty text.

## Document Generation (docs.py)

**Location:** `/workspace/projects/grey-docs/00-engine/docs.py`
**Templates:** `/workspace/projects/grey-docs/00-engine/templates/`

### How It Works

1. Finds the `*DATA*` spreadsheet in the parent directory
2. User selects AO number (1–15)
3. User selects document type
4. Loads the selected AO sheet
5. Maps all row-2 cells to template variables (see table above)
6. Overrides description of works from Form sheet (bypasses formula cache issues)
7. Renders `.docx` template with Jinja2 variables
8. Removes empty bullet points
9. Converts to PDF via Gotenberg

### Document Types (49 templates)

| Code | Document |
|------|----------|
| 01 | Letter of Appointment (BO / AO) |
| 02 | Notices Served (7 section combos × 4 variants = 28 templates) |
| 03.1 | 10 Day Letter AO (Corr/Prop × Letter/Email) |
| 05.1 | Conditional Consent / Conditional Consent (BO Signature) |
| 06 | Security for Expenses Notice |
| 07 | Special Foundations Consent |
| 08 | Section 10.7 Letter |
| 12.1 | Award D1 / D2 (+ Custom variants) |
| 12.2–12.9 | Award Cover Letters (BO/AO × Corr/Prop × Letter/Email ± Invoice) |
| 14.1 | Schedule of Condition |
| 14.2–14.9 | SoC Cover Letters (same variants as Award) |
| 15.1 | Addendum Award D1 / D2 |
| 16 | Notice for Access (Section 8) |
| 18 | Licence to Alter |
| 50 | Invoice |

### Notice Template Auto-Selection

Based on Form sheet Yes/No values at rows 72/81/101:
- S1 only → `(s1)` template
- S2 only → `(s2)` template
- S6 only → `(s6)` template
- S1+S2 → `(s1, s2)` template
- S1+S6 → `(s1, s6)` template
- S2+S6 → `(s2, s6)` template
- S1+S2+S6 → `(ALL)` template

Each combined with variant: Prop, Prop (Email), Corr, Corr (Email).

### Validation

Before generating notices, the system validates that Yes/No flags match actual description content:
- S1=Yes but no descriptions → warning
- S1=No but has descriptions → warning
- Same for S2 and S6

### Formula Cache Issue

AO sheets use formulas referencing the Data sheet. If Excel wasn't opened and saved, cached values may be stale. The system works around this by reading description of works directly from the Form sheet instead of relying on AO sheet formula results.

## Key Patterns

### Column Offset Rule
**Everything shifts 8 columns right per AO.** This is the fundamental pattern:
- AO 1: base column
- AO 2: base + 8
- AO 3: base + 16
- AO n: base + (n-1) × 8

### Directory Structure Per Project
```
project-folder/
├── 00.0 - DATA - {BO_Name}_{AO_Name} - {Address}.xlsx
├── AO 1/          ← generated documents for AO 1
├── AO 2/          ← generated documents for AO 2
├── ...
└── 00-engine/     ← symlink or copy of generation engine
```

### File Naming Convention
DATA files follow: `00.0 - DATA - {BO Surname}.{BO First}_{AO1 Surname}.{AO1 First} - {Property Address}.xlsx`

## Sample Data (Hardy/Cheng project)

- **Building Owner:** Mr Benjamin Hardy & Ms Alexis Ruth Barr (Freeholders)
- **BO Property:** 50 Lancaster Road, London, N4 4PR
- **AO 1:** Mr Oliver Edward Tsa Cheng & Mrs Laura Rose Cheng (Freeholders)
- **AO 1 Property:** 48 Lancaster Road, London, N4 4PR
- **Notices:** S1=No, S2=Yes, S6=Yes
- **S2 works:** Cut pockets into party wall for steel beams, install fixings/flashings, temporarily expose party wall
- **S6 works:** Excavations and groundworks for laying foundations
