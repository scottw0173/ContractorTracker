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

## 2026-10-03 — PTO and time off

`PTO` consumes the annual PTO allowance, currently 15 days per year, for an
otherwise working day. `TIME_OFF` represents a normally non-working day and
does not consume PTO. `NO_RESPONSE` remains unknown and represents neither.

`PTOFraction` tracks PTO consumption independently of work status so it can
be totaled and later support partial-day PTO. A PTO selection currently uses
WorkFraction = 0 and PTOFraction = 1; all other statuses use PTOFraction = 0.
`HALF_DAY` remains WorkFraction = 0.5 and consumes no PTO.

## 2026-10-03 — Conditional automatic finalization

Automatic `PENDING -> NO_RESPONSE` finalization writes the complete record
only if DynamoDB's stored status is still `PENDING`. A conditional conflict
is expected and does not block today's creation. This prevents automatic
finalization from overwriting a concurrent user response.

## 2026-10-03 — Missing previous-day records

A missing previous-day record represents missing system data, not proof of
non-response. Only an existing `PENDING` record can become `NO_RESPONSE`;
the daily runner does not create synthetic records for missing days.

## 2026-10-03 — Signed email response capabilities

Email response date/status claims use versioned HMAC-SHA256 signed bearer
tokens rather than JWT or server-side token storage. The wire format is
unpadded base64url(JSON payload) followed by a dot and base64url(HMAC), with
the HMAC computed over the exact encoded payload string.

## 2026-10-03 — No token expiration

Tokens currently do not expire because late responses and corrections from
older emails are intentionally supported. The status handler will determine
which record mutations an old valid token may perform.

## 2026-10-03 — Token security scope

Token contents are signed but not encrypted. The signature protects the
date/status from modification without the secret; it neither hides the
contents nor authenticates a human identity. Treat tokens as bearer
capabilities: anyone possessing a valid token can present its signed claims.
