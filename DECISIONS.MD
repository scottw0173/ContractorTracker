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

## 2026-10-03 — Email response URL policy

The email builder accepts absolute HTTP(S) response URLs, preserves paths
and unrelated query parameters, and rejects credentials, fragments, and
preexisting token/date/status parameters. Each choice adds only its signed
token as authorization and selection input. Production links should use HTTPS.

## 2026-10-03 — Email delivery ordering

Daily orchestration sends the SES email first, then persists
EmailSentAt. This provides at-least-once delivery: a crash after SES accepts
the email but before the timestamp is saved may cause a duplicate on retry.
That is preferable to saving the timestamp first and suppressing the only
prompt if sending fails. SES acceptance does not guarantee inbox delivery.

EmailSentAt persistence updates only the timestamp attribute,
not the entire DayRecord, so a rapid concurrent user response is not overwritten.
It uses a conditional DynamoDB UpdateItem requiring the day to exist and the
timestamp attribute to be absent. Existing timestamps are never replaced.

## 2026-10-03 — Daily prompt eligibility and recovery

Send only while today's status is PENDING and EmailSentAt is absent. An
existing user response suppresses a retry prompt even without EmailSentAt:
SES may have succeeded, timestamp persistence failed, and the user responded
before the retry. Do not fabricate a timestamp for that response.

## 2026-10-03 — Concurrent email delivery

Delivery remains at-least-once. Concurrent daily workers can both send before
either persists EmailSentAt. This small residual duplicate-delivery possibility
is accepted instead of adding a distributed claim or locking protocol.
Timestamp conditional conflicts are expected and complete the run successfully.


## 2026-10-03 — Narrow conditional user responses

User responses use conditional DynamoDB UpdateItem rather than replacing
whole records. Only status, work_fraction, pto_fraction, responded_at,
response_source, and has_been_changed change. Email timestamps, finalization
timestamps, weekend information, and date metadata remain untouched.

Submission verifies the token once, then makes at most three consistent
read/apply/conditional-update attempts. Conflicts trigger rereading so
finalization races become late responses and concurrent corrections retain
correction semantics. Response writes always condition on the expected status;
expected user responses also require the expected RespondedAt, serialized as
RFC3339Nano. This prevents change-away-and-back ABA sequences with changed
response timestamps from accepting stale metadata. PENDING and NO_RESPONSE
require only the status comparison. No general version attribute is needed.
This assumes distinct response timestamps for actual transitions; timestamp
collisions do not distinguish otherwise matching states.

Repeating a response still performs a conditional update, but leaves the
original RespondedAt and correction flag unchanged. Returned records are
applied read snapshots; unrelated concurrent attributes may be newer in
persistence. Preview is read-only and never mutates a record.


## 2026-10-03 — HTTP response confirmation

Email-link GET requests are always read-only and render confirmation; only
an explicit POST calls submission. The bearer token is a hidden form field
in an application/x-www-form-urlencoded body, never in the form action URL.
Forms use the current local request path without query parameters. No
redirects or external resources are used. Dynamic content uses html/template.

POST accepts one non-empty token, rejects duplicate token fields, ignores
unrelated form fields, and limits decoded form bodies to 16 KiB. Function
URL base64 bodies are decoded before parsing. Unsupported or malformed
Content-Type headers return 415; malformed form data returns 400.

Pages use Cache-Control: no-store, Referrer-Policy: no-referrer, nosniff,
and a restrictive CSP with same-origin form actions because GET links carry
bearer capabilities. Tokens appear only in the confirmation's hidden field.

HTTP mapping: invalid input/token -> 400; missing record -> 404; exhausted
concurrency conflict -> 409; unsupported method -> 405 with Allow: GET, POST;
internal failure -> 500. Every failure returns generic HTML and a nil Go
error, including internal service failures, so Function URLs deliver the
intended page. Logging remains deferred to a later integration slice.


## 2026-10-03 — Lambda integration and builds

Go entrypoints construct the SDK clients and existing services once at cold
start. Application configuration reads environment variables without loading
AWS configuration. Only the runtime boundary supplies time.Now. Timezone data
is embedded so application dates do not depend on runtime zoneinfo files.

Go Lambdas use provided.al2023 ZIP deployment on x86_64. The shared-module,
two-binary project uses SAM Makefile builds producing bootstrap with
lambda.norpc; no Docker or duplicated modules are needed.

## 2026-10-03 — Initial AWS deployment choices

The Function URL uses AuthType: NONE with no CORS. Signed response bearer
tokens and explicit GET-to-POST confirmation provide application authorization;
SAM generates the public Function URL resource policies.

The shared HMAC secret is initially a required NoEcho CloudFormation parameter
injected into both function environments. Avoiding Secrets Manager cost and
complexity is an operational choice for this personal application, not a
general recommendation. Secrets and samconfig.toml must never be committed.

EventBridge Scheduler uses ScheduleV2 with the application timezone. The
schedule expression is a required deployment choice, and the initial schedule
state is DISABLED until manual testing. No DLQ, custom retries, or schedule
group is configured. Its flexible time window is OFF.

The authoritative DynamoDB table uses Retain for deletion and replacement.
Function roles receive only their current table operations and standard Lambda
logging. SES SendEmail is limited to the configured verified sender identity.
The initial SES recipient defaults to the mailbox simulator, not a real inbox.


## 2026-10-03 — Google Sheets authorization boundary

Use service-account credentials decrypted from the configured SSM parameter,
with only the spreadsheets read/write OAuth scope and no Drive scope. The
spreadsheet ID selects the existing target; it does not restrict OAuth access.
Google sharing permissions must grant the service account access to that file.
Client construction is offline; token acquisition occurs lazily on API use.

## 2026-10-03 — Incremental Sheets projection

DynamoDB remains authoritative. Stream notifications and manual {year, date}
events read each day with
strongly consistent GetDay and upsert it into Daily Log YYYY. Summary remains
an existing separate worksheet; the unyearly Daily Log tab is not required.
Yearly tabs are created on demand. Application-owned headers are initialized
only when blank; nonblank schema mismatches and duplicate dates fail rather
than silently choosing or overwriting data. No full-year rebuild or sort occurs.

RAW values retain strings, numeric fractions, and boolean flags; nil fractions
and zero timestamps become empty cells. Present timestamps use UTC RFC3339Nano.
Reserved concurrency was removed after the account rejected its reservation
under the concurrency quota. No replacement reservation or lock is introduced.
Default stream shard ordering does not serialize all Sheets writers: different
shards, manual invocations, or external writers can race on tab creation/append
or project an older read snapshot after a newer one. Manual repair remains
available. No Summary formulas are implemented.

SSM credentials are loaded on each invocation and the Google client retains
only that invocation's context, avoiding expired contexts during token refresh.
A retry rereads DynamoDB and locates the existing date row, including after an
ambiguous successful append. A response updated during projection may require
a later stream notification or manual invocation to refresh the snapshot.

## 2026-10-03 — Stream notifications reread authoritative state

DayTable uses KEYS_ONLY DynamoDB Streams. INSERT/MODIFY notifications supply
only year/date; strongly consistent GetDay fetches current state before each
UpsertDay. NewImage and OldImage never supply projected values. REMOVE is
ignored; spreadsheet row deletion is not part of this projection contract.

The SAM DynamoDB event starts at TRIM_HORIZON, batches at most 10 records,
and keeps default per-shard parallelization. Records are processed sequentially
within each invocation. Errors fail the whole batch and use default Lambda
stream retry behavior; successful earlier projections can repeat on retry.
Missing current days remain errors, consistent with the manual path. No DLQ,
custom retry, batching delay, partial-batch response, or reconciliation is added.
SAM supplies its standard DynamoDB stream-read execution policy; application
GetItem remains restricted to DayTable. Manual keys remain supported for repair.
