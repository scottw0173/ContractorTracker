# Architecture Decisions

## 2026-10-03 — DynamoDB key structure

Use:
- PK: `year`
- SK: ISO `date`

Reason:
The dataset is tiny, and grouping by year allows a full year's records
to be queried in chronological order with a single DynamoDB query.

## 2026-10-03 — Non-response semantics

`NO_RESPONSE` has no WorkFraction.

A non-response means the amount worked is unknown, not zero.

Weekend status is stored separately as `IsWeekend`.

## 2026-10-03 — Corrections

A late response replacing `NO_RESPONSE` is not considered a correction.

Changing one user-selected work status to another sets
`HasBeenChanged = true`, permanently.