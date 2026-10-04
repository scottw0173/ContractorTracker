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
three-Lambda-binary project uses SAM Makefile builds producing bootstrap with
lambda.norpc; local admin CLIs remain separate. No Docker or duplicated modules
are needed.

## 2026-10-03 — Initial AWS deployment choices

The Function URL uses AuthType: NONE with no CORS. Signed response bearer
tokens and explicit GET-to-POST confirmation provide application authorization;
SAM generates the public Function URL resource policies.

The shared HMAC secret is retrieved from the existing CT-TokenSecret SSM
SecureString parameter once per daily/status cold start with decryption. Only
the configurable parameter name is passed through CloudFormation/Lambda settings;
secret values and private deployment files must never be committed or logged.
No Secrets Manager dependency is introduced.

EventBridge Scheduler uses ScheduleV2 with the application timezone. The proven
production default is cron(0 6 * * ? *) at 6:00 AM America/Mazatlan, ENABLED.
Expression, timezone, and state remain overrideable. No DLQ, custom retries,
or schedule group is configured; its flexible time window is OFF.

The authoritative DynamoDB table uses Retain for deletion and replacement.
Function roles receive only their current table operations and standard Lambda
logging. SES SendEmail is limited to the configured verified sender identity.
The configured production SES recipient is preserved; the full production path
has now been tested end-to-end.


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
available. Summary is a separate formula-driven selected-year presentation.

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
and keeps default per-shard parallelization. Validate all INSERT/MODIFY keys
before any synchronization, then deduplicate per invocation in first-seen order.
Each unique key runs GetDay -> UpsertDay once, sequentially. There is no
persistent deduplication state; later invocations reread current state. Errors
fail the whole batch and use default Lambda stream retry behavior; successful earlier projections can repeat on retry.
Missing current days remain errors, consistent with the manual path. No DLQ,
custom retry, batching delay, partial-batch response, or reconciliation is added.
Stream polling permissions are explicit in SheetSyncFunctionRole: DescribeStream,
GetRecords, and GetShardIterator on DayTable.StreamArn, and ListStreams on the
DayTable stream ARN pattern. GetItem remains a separate table-only statement.
Supplying an explicit role prevents SAM's generated role from attaching the
broad AWSLambdaDynamoDBExecutionRole policy. The logical ID matches SAM's
previous generated role. Manual keys remain supported for repair.

## 2026-10-04 — Supported administrative corrections

Administrative business-state corrections use the local cmd/admin-correct CLI
with existing operator AWS credentials, not direct DynamoDB console field edits.
Console edits bypass application transition semantics and are unsupported.
The CLI reuses ApplyUserStatus and UpdateUserResponseIfCurrent, with current UTC
time supplied at its boundary. Initial and late responses preserve correction
metadata; changes between user statuses permanently set HasBeenChanged. Late
responses/corrections remain LATE_USER and preserve FinalizedAt. HasBeenChanged
is application state rather than a tamper-proof flag or an append-only audit log.

The CLI previews each proposed transition and requires explicit confirmation.
It retries at most three conditional write attempts, rereading and reconfirming
on conflicts; matching status is a no-op with no write. Its small interactive
loop is separate from signed submission, which intentionally still conditionally
writes repeated responses. Domain logic and persistence expressions stay shared.
Successful DynamoDB corrections use the existing Stream projection; no direct
Sheets calls, administrative public endpoint, or new IAM/SAM resources are added.

## 2026-10-04 — Yearly Daily Log presentation

Each projected yearly tab has row 1 frozen, row banding covering A1:K370,
and A:K auto-sized after successful row writes. Existing covering bands,
including larger ranges and their colors, remain untouched; yearly tabs need
not share a palette. Missing banding uses subdued defaults. A partial overlapping
band returns an error for manual adjustment rather than modifying existing
formatting or introducing another overlapping band. Yearly presentation does
not style Summary; the dashboard has its own presentation contract.

Metadata reads request only sheet identity, frozen row count, and band ranges,
without grid cell data. Each invocation checks presentation again, so retries
can finish initialization after an accepted request with a lost response without
adding another band. Formatting is presentation only, never authoritative state.
Existing cross-writer races remain; this adds no locking mechanism.

## 2026-10-04 — Selected-year Summary dashboard

Summary reports exactly one calendar year, selected in B3 from ascending exact
Daily Log YYYY worksheet names. Preserve valid past-year selections, default
blank/invalid selections to newest, and select a newly created annual tab once.
Creation, year selection, and dropdown validation share one atomic Google batch
so the switch survives later projection failures and ambiguous create responses.
Formulas reference the selected tab; no DynamoDB reconciliation or additional
worksheet is needed. Spreadsheet TODAY() is reporting-only and uses its timezone.

Current corrected record state drives all metrics. Work Eq. sums Work Fraction,
PTO Used sums PTO Fraction with no assumed entitlement, all NO_RESPONSE statuses
count equally, and Changed=true is descriptive metadata rather than an audit log.
Monthly January–December totals replace weekly averages and match headline
semantics, using ISO text month prefixes rather than native date-cell coercion.
Historical finalization and response-source metadata do not affect counts.

The sorted Date/Day PTO list reserves 15 data rows as expected annual display
capacity, not entitlement enforcement. A visible overflow warning directs the
viewer to Daily Log when more records exist; totals still include every record.
Records Through computes the maximum logical date regardless of physical order.

Owned labels/formulas are initialized only when blank and checked using entered
content, not calculated results. Unexpected nonblank content fails before Summary
writes. B3 is user-controlled; unrelated cells remain untouched. One batch applies
narrow value, validation, and formatting changes; repeats only repair differences
and auto-size columns. Summary follows successful Daily Log upserts, so dashboard
failures leave authoritative projection rows intact for retry/manual repair.

## 2026-10-04 — Missing historical days use administrative backfill

The local admin-backfill CLI creates only absent authoritative days, using the
pure NewAdminBackfillDay helper and existing conditional CreateDay. A strong
initial read blocks existing records, and the conditional create protects races.
Preview and exact y/yes confirmation are required; no overwrite or live write
is implied by implementation/testing. Existing days use admin-correct instead.
Direct DynamoDB business-field construction/editing remains unsupported.

ADMIN_BACKFILL distinguishes reconstructed history from email responses.
RespondedAt records the current UTC administrative entry time; EmailSentAt and
FinalizedAt remain zero, and HasBeenChanged starts false. Normal domain rules
supply fractions and weekend context. Later different-status corrections set
HasBeenChanged and use normal USER/LATE_USER rules; same-status administrative
corrections remain no-ops. Successful INSERTs use the existing Stream projection;
manual SheetSync is still a separate projection-repair path. No infrastructure
or administrative public endpoint is introduced.

## 2026-10-04 — Token-secret SSM retrieval

A neutral internal/ssmsecret loader owns only GetParameter with decryption and
nonblank confidential bytes. Google credentials remain in their existing loader.
Daily/status config requires TOKEN_SECRET_PARAMETER and ignores TOKEN_SECRET;
AWS retrieval occurs only at cold start, before token.New, never per request.
Exact bytes are preserved so old signed links verify with the unchanged secret.

SSM errors retain typed causes for errors.Is/errors.As but display generic text,
preventing arbitrary SDK error text from leaking into initialization logs. Never
log an explicitly unwrapped retrieval cause. Missing output/parameter/value,
blank values, nil clients, and blank names fail initialization.

CloudFormation accepts TokenSecretParameter (default CT-TokenSecret), not a
secret-value parameter. Each role grants GetParameter on that exact parameter
ARN only. Production expects SecureString with normal SSM-managed encryption;
a future customer-managed KMS key would need narrowly scoped Decrypt/key access.
Warm environments keep their initialized secret, so this is not a rotation
protocol. Saved deployment overrides must remove the old TokenSecret setting
and explicitly select the enabled 6 AM production schedule when updating a stack.
