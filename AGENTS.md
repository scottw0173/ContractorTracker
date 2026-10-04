# ContractorTracker

## Purpose

ContractorTracker is a small personal automation for recording daily contractor work status.

The system should:

- Ask the user for their work status once per day by email.
- Record the response in DynamoDB.
- Distinguish full days, half days, PTO, time off, and non-responses.
- Preserve whether a non-response occurred on a weekend.
- Use DynamoDB as the authoritative source of truth.
- Periodically project the DynamoDB data into a Google Sheet for human-readable reporting.

This is intentionally a small system. Prefer simple, explicit code over generalized frameworks or speculative abstractions.

---

## Development Philosophy

This project is being built with AI assistance, but it should remain understandable and maintainable by the human developer.

Follow these rules:

1. Make small, reviewable changes.
2. Do not implement unrelated future functionality unless explicitly requested.
3. Prefer straightforward Go over clever abstractions.
4. Explain non-obvious design choices after making them.
5. Avoid unnecessary interfaces, factories, dependency-injection frameworks, or generic repository frameworks.
6. Do not introduce infrastructure merely because it might be useful later.
7. Preserve clear separation between domain logic and AWS/Google SDK code.
8. Prefer pure functions for business-state transitions when practical.
9. Add tests for meaningful state-transition logic.
10. Run `gofmt` and `go test ./...` after code changes.
11. Do not silently change agreed architecture. Call out a proposed architectural change before implementing it.
12. Stop after completing the requested slice of work. Do not continue building later phases automatically.

The human developer should be able to read, debug, modify, and extend everything produced here.

---

# Technology

Primary language:

- Go

Infrastructure:

- AWS Lambda
- AWS SAM
- Amazon DynamoDB
- Amazon SES
- EventBridge Scheduler
- Lambda Function URL
- Google Sheets API

Use AWS SDK for Go v2.

Lambda deployment should use the current supported Go custom runtime approach rather than adding Docker unless a future requirement makes containers necessary.

Do not place the Lambda functions in a VPC unless a future dependency explicitly requires it.

The integration boundary is cmd/daily-worker, cmd/status-handler, and cmd/sheet-sync. It loads
application environment settings, then the AWS SDK v2 default configuration,
and constructs AWS clients once at cold start. The sheet-sync Google client
is constructed per invocation with freshly loaded SSM credentials. Business packages
continue to receive explicit timestamps and injected clients/dependencies.

SAM uses provided.al2023, bootstrap, and x86_64 ZIP deployment with root
Makefile targets for all three functions. ScheduleV2 uses the application timezone
and defaults to ENABLED at 6:00 AM America/Mazatlan. The Function URL uses AuthType: NONE with signed
bearer tokens as application authorization. The DynamoDB table is retained
on stack deletion/replacement. The production path has been proven end-to-end.
Daily/status config carries TOKEN_SECRET_PARAMETER, defaulted by SAM to the
existing CT-TokenSecret SecureString parameter. These two entrypoints retrieve
and decrypt the value once at cold start using internal/ssmsecret, then construct
the existing token signer. No TOKEN_SECRET environment or secret-value
CloudFormation parameter remains. Preserve the existing bytes so old links stay
valid. Both roles get only ssm:GetParameter on that configured parameter; a future
customer-managed KMS key additionally needs scoped kms:Decrypt. Displayed loader
errors hide SDK error text; unwrapped causes must not be logged. The Google
credential loader remains independent, with per-invocation retrieval unchanged.

Secrets and generated/private deployment files must not be committed.
The checked-in samconfig.toml contains only stable non-sensitive deployment
mechanics; no email values, secrets, parameter overrides, role ARN or artifact
bucket belong in it. EmailFrom and EmailTo are required external parameters.

GitHub Actions verifies pull requests without AWS credentials. Verified pushes
to main rebuild and deploy noninteractively to contractor-tracker in us-east-2.
GitHub OIDC trust uses the exact immutable subject
repo:scottw0173@156988004/ContractorTracker@1403695477:ref:refs/heads/main
and audience sts.amazonaws.com. Only deploy requests id-token: write.
Repository Variables AWS_DEPLOY_ROLE_ARN, EMAIL_FROM and EMAIL_TO are validated
without printing values. Variables are not masked; email-only Secrets references
are an optional privacy choice. No AWS access keys are stored in GitHub.

infra/github-actions-bootstrap.yaml is independent of the application stack:
optional GitHub provider creation/reuse, private retained artifact bucket, OIDC
deploy role and dedicated CloudFormation execution role. Deploy can package,
operate only the app stack, and pass only the execution role. Execution manages
only current app services and generated role patterns, excluding bootstrap IAM.
CreateEventSourceMapping requires Resource "*" but is function-ARN conditioned.
No deployment, external setup or repository-settings mutation is implied by
editing these files; the operator performs bootstrap and Variables setup.

---

# High-Level Architecture

There are three Lambda functions. The sheet-sync function consumes DayTable
KEYS_ONLY stream notifications for inserts and modifications, deduplicates keys
in first-seen order within each batch, and rereads each day
with strongly consistent GetDay, and upserts it into Daily Log YYYY. Stream
images are never projected. SheetSync accepts typed DynamoDB events only.
Summary must already exist; there is no full-year reconciliation.

## daily-worker

Triggered once per day by EventBridge Scheduler.

Responsibilities:

1. Determine today's and yesterday's dates using the configured application timezone.
2. Load yesterday's record.
3. If yesterday exists and is still `PENDING`, convert it to `NO_RESPONSE`
   using a conditional write requiring the stored status still to be `PENDING`.
   Leave missing records absent and continue after conditional conflicts.
4. Google Sheets projection is separate and stream-driven; the daily worker
   does not synchronize spreadsheets.
5. Ensure today's record exists without replacing existing data; reread an
   existing record before deciding whether to prompt.
6. Build and send today's status email only while its status is `PENDING`
   and `EmailSentAt` is absent.
7. After SES accepts the email, conditionally persist only `EmailSentAt`
   using UpdateItem, requiring the day to exist and the timestamp to be absent.

The daily process should be idempotent where practical.

A Lambda retry must not create duplicate daily records.

A Lambda retry should not send another prompt when `EmailSentAt` is present
or today already has a user response. Delivery is at-least-once: a failed
timestamp write or concurrent workers can cause duplicate emails; no
distributed send lock is used.

## status-handler

Invoked through a Lambda Function URL.

The email contains links for status selections.

A GET request MUST NOT directly modify the work record because email-security software may automatically follow links.

Expected flow:

1. User clicks an email status link.
2. GET request validates the request and displays a small confirmation page.
3. User confirms the selection.
4. POST request updates DynamoDB.

The response handler should allow:

- normal responses;
- late responses after a `NO_RESPONSE`;
- correcting a previous user response;
- repeated/idempotent submission of the same status.

Response preview is read-only. Submission updates only response attributes
with a conditional UpdateItem requiring an existing day and unchanged status.
For an expected user response, RespondedAt must also match the read snapshot
to reject stale metadata after a status changes away and back.
It rereads and reapplies on conflict, up to three total attempts. Repeated
selections retain RespondedAt; email/finalization timestamps and date metadata
are preserved independently.

The respondweb adapter uses AWS Function URL event types. GET only previews
and renders an escaped confirmation form. POST submits exactly one bearer
token from a bounded application/x-www-form-urlencoded body, including
base64-encoded event bodies. Forms post to the current local path without
query parameters. Pages use no-store, no-referrer, nosniff, and a restrictive
CSP; success and error pages contain no token or internal error details.

## Local administrative backfill

Use cmd/admin-backfill to create a missing historical authoritative day, not to
correct an existing day or merely repair a Sheets projection. Validate required
--table/--year/--date/--status and reject positional arguments. Read through the
existing strongly consistent GetDay; existing records block creation and direct
the operator to admin-correct. Preview the final record and require exact y/yes
(case-insensitive) before conditional CreateDay. A concurrent insert prevents
creation; no overwrite or correction retry is attempted.

tracker.NewAdminBackfillDay reuses NewPendingDay and ApplyUserStatus to derive
calendar/weekend and status/fraction values. Backfills have ADMIN_BACKFILL source,
false HasBeenChanged, UTC administrative entry time in RespondedAt, and zero
EmailSentAt/FinalizedAt. They do not fabricate historical email-response times.
Later different-status corrections use normal transition/source semantics;
same-status administrative corrections do not write. The existing DynamoDB
Stream projects successful INSERTs without direct Sheets calls. Use existing
local AWS credentials; no new configuration, Lambda, or SAM resource is needed.
Direct manual construction/editing of DynamoDB business fields is unsupported.
SheetSync is automatic stream projection only; neither admin CLI invokes it.

## Local administrative corrections

Use cmd/admin-correct for administrative business-state corrections rather than
direct DynamoDB field edits, which bypass supported transition semantics.
This local CLI validates --table/--year/--date/--status, reads the existing day,
uses tracker.ApplyUserStatus, previews the resulting metadata, and requires
explicit y/yes confirmation before UpdateUserResponseIfCurrent. It uses existing
local AWS credentials; no administrative Lambda, endpoint, or SAM resource exists.

Conditional conflicts reread/reapply, with fresh preview and confirmation, up to
three write attempts. Same-status requests perform no write and retain response
metadata. The outer CLI supplies current UTC time; no arbitrary timestamp input
is supported. The existing Stream handles Sheets projection after a correction.
HasBeenChanged is application state describing changes through supported user
status transitions, not tamper-proof data or an audit log. No audit system exists.

---

# Application Timezone

The application timezone must be explicit.

Use an environment variable:

`APP_TIMEZONE`

Initial expected value:

`America/Mazatlan`

Do not derive calendar dates directly from Lambda's default system timezone.

---

# DynamoDB

DynamoDB is the source of truth.

Use one item per calendar date.

## Primary Key

Partition key:

`year`

Sort key:

`date`

Example:

```text
year = 2026
date = 2026-10-03
```

`date` must use ISO `YYYY-MM-DD` format so records naturally sort chronologically.

There is no current need for:

- GSIs
- LSIs
- additional tables
- monthly partitions
- user/account partitions
- audit-history tables

The dataset is intentionally tiny.

---

# Day Record

Conceptual record:

```go
type DayRecord struct {
    Year           int
    Date           string
    Status         Status
    WorkFraction   *float64
    PTOFraction    float64
    IsWeekend      bool

    EmailSentAt    time.Time
    RespondedAt    time.Time
    FinalizedAt    time.Time

    ResponseSource ResponseSource
    HasBeenChanged bool
}
```

This is the agreed conceptual schema. Minor Go-level improvements may be proposed, but do not materially change its meaning without discussing the change first.

Timestamp strings should use a consistent RFC3339 representation.

---

# Status Values

Allowed statuses:

```text
PENDING
FULL_DAY
HALF_DAY
PTO
TIME_OFF
NO_RESPONSE
```

Do not use `WEEKEND` as a status.

Weekend information belongs in `IsWeekend`.

## Work and PTO Fractions

`PTO` consumes the contractor's annual PTO allowance, currently 15 days per
calendar year, for a day that otherwise would have been a working day.
`TIME_OFF` does not consume PTO: it represents a normally non-working day,
such as a weekend, holiday, or company closure.

```text
Status        WorkFraction  PTOFraction
PENDING       nil           0
FULL_DAY      1.0           0
HALF_DAY      0.5           0
PTO           0.0           1.0
TIME_OFF      0.0           0
NO_RESPONSE   nil           0
```

Selecting PTO currently means a full PTO day. There is no half-day-PTO user
status. PTOFraction allows PTO consumption to be totaled independently of
work status and leaves room for partial-day PTO later.

`NO_RESPONSE` must NOT be represented as zero hours or zero work fraction.

A lack of response means the work amount is unknown. It must never be
interpreted as PTO or TIME_OFF.

---

# Response Sources

Allowed response sources:

```text
USER
LATE_USER
AUTO_FINALIZE
ADMIN_BACKFILL
```

Expected meanings:

`USER`
: A user response before the day's record had been automatically finalized as `NO_RESPONSE`.

`LATE_USER`
: A user response after the record had previously been automatically finalized as `NO_RESPONSE`.

`AUTO_FINALIZE`
: The daily worker found a previous `PENDING` record and automatically changed it to `NO_RESPONSE`.

`ADMIN_BACKFILL`
: A missing historical day reconstructed through admin-backfill. RespondedAt is
  administrative entry time; no email or finalization event is fabricated.

If a user corrects an existing user-selected value, it remains a user-originated response.

If `FinalizedAt` indicates the record previously timed out, subsequent user changes should remain distinguishable as late user responses.

---

# State Transitions

New daily record:

```text
<none> -> PENDING
```

Normal responses:

```text
PENDING -> FULL_DAY
PENDING -> HALF_DAY
PENDING -> TIME_OFF
PENDING -> PTO
```

Automatic finalization:

```text
PENDING -> NO_RESPONSE
```

Late responses:

```text
NO_RESPONSE -> FULL_DAY
NO_RESPONSE -> HALF_DAY
NO_RESPONSE -> TIME_OFF
NO_RESPONSE -> PTO
```

User corrections are allowed:

```text
FULL_DAY -> HALF_DAY
FULL_DAY -> TIME_OFF
FULL_DAY -> PTO

HALF_DAY -> FULL_DAY
HALF_DAY -> TIME_OFF
HALF_DAY -> PTO

TIME_OFF -> FULL_DAY
TIME_OFF -> HALF_DAY
TIME_OFF -> PTO

PTO -> FULL_DAY
PTO -> HALF_DAY
PTO -> TIME_OFF
```

Changing an already user-selected status to a different user-selected status
sets HasBeenChanged permanently to true, including corrections to or from PTO.
Initial responses and late responses replacing NO_RESPONSE are not corrections.

Submitting the currently selected status again should be harmless.

Do not create an append-only history system at this stage.

---

# Weekend Behavior

The automation runs every calendar day, including weekends.

When creating a daily record, calculate and persist:

```text
IsWeekend = Saturday or Sunday
```

A weekend `NO_RESPONSE` remains a `NO_RESPONSE`.

Do not automatically turn weekend non-responses into `TIME_OFF`.

Summary counts all current NO_RESPONSE records equally; weekend context does
not change reporting classifications.

---

# Google Sheets

Google Sheets is a reporting projection, not the database.

internal/sheets loads SSM credentials, constructs clients, and incrementally
upserts one authoritative DayRecord into Daily Log YYYY. TABLE_NAME selects
the DynamoDB table; GOOGLE_CREDENTIALS_PARAMETER and GOOGLE_SPREADSHEET_ID select
the decrypted SSM credential and existing spreadsheet. Google clients use only
the Sheets read/write scope, without Drive or default credential discovery.

Summary must exist separately. Missing yearly tabs are created; blank headers
are initialized, valid headers are retained, and nonblank mismatches fail.
Read only the Date column below row 1: exactly one match is updated, no match
is appended, and duplicate dates fail. No unrelated rows are rewritten or sorted.
All writes use RAW values; nil work fractions and zero timestamps become blanks.
Timestamps use UTC RFC3339Nano, and numeric fractions and boolean flags keep
their types. The schema is Date, Day, Status, Work Fraction, PTO Fraction,
Weekend, Email Sent At, Responded At, Response Source, Finalized At, Changed.

Yearly Daily Log presentation freezes row 1, preserves existing banding that
covers A1:K370 (including its colors), and auto-sizes A:K after each successful
row upsert. Missing banding is added with subdued defaults; yearly color schemes
need not match. Partial overlapping bands that do not cover A1:K370 require
manual adjustment and return an error instead of adding overlapping formatting.
Presentation is not business state; Summary has a separate reporting layout.

cmd/sheet-sync validates the positive year and exact matching ISO date before
using the existing strongly consistent GetDay. It loads credentials and creates
the Google client lazily on first projection per invocation and reuses it across
a stream batch; empty/REMOVE-only batches load no Google credentials. The SAM
DynamoDB event uses TRIM_HORIZON and batch size 10 with default per-shard
parallelization. INSERT/MODIFY keys are all validated before synchronization,
deduplicated per invocation in first-seen order, and each unique key follows
GetDay -> UpsertDay once. REMOVE is ignored. An explicit SheetSyncFunctionRole
supplies stream-read IAM: DescribeStream/GetRecords/GetShardIterator on the
DayTable stream ARN and ListStreams on DayTable's stream ARN pattern. GetItem
is a separate DayTable-only permission; no managed DynamoDB policy is used.
Errors fail the whole batch; no partial-batch response or custom retry is used.
No reserved concurrency is configured because this account rejected it under
its concurrency quota. Default shard ordering is not a global Sheets writer
lock; concurrent shards or external writers can race on tab creation or append.
No replacement lock or full-year sync exists.

SheetSync has no supported direct per-date invocation. The production stream
projection has been proven end-to-end. admin-backfill creates missing authoritative
days; admin-correct changes existing authoritative days; their writes trigger
automatic projection. Inconsistencies that cannot be repaired by legitimate
authoritative changes require a future explicit reconciliation/replay mechanism,
not custom Lambda events or fabricated business-state changes.

Summary is a formula-driven dashboard for one selected calendar year, not an
audit log. B3 lists available exact positive four-digit Daily Log YYYY years in
ascending order. Preserve a valid selection; blank/invalid defaults to newest.
A newly created annual tab becomes selected once in the same atomic Google
batch as creation and dropdown validation; later upserts preserve past years. Current record state drives every total. All NO_RESPONSE
statuses count equally; no weekend or historical-finalization filtering occurs.
PTO Used sums PTO Fraction without assuming entitlement or PTO remaining.

Summary layout: title A1; Reporting Year A3:B3; Records Through D3:E3; Today's
Status A4:B4; six headline metrics A6:F7; monthly table A11:G24 beneath A10;
PTO title A27, overflow warning D27, and Date/Day list A28:B43. The monthly
breakdown replaces weekly averages and has January–December plus TOTAL, with
Month, Work Eq., Full, Half, PTO, Time Off, No Response only. PTO lists sorted
current PTO dates within the expected annual 15-day display capacity; overflow
is explicit and does not truncate totals or Daily Log data.

Summary formulas dynamically reference B3 and operate on RAW ISO date strings.
Records Through is the maximum logical date, not the last physical row. Today's
Status uses Sheets TODAY() for the selected current year only; the spreadsheet
timezone governs that reporting display. No Summary data becomes business state.

After successful per-date upserts, initialize blank required Summary labels and
formulas, maintain dropdown values, and apply basic presentation with narrow
field updates. Inspect entered content rather than calculated formula results;
incompatible nonblank owned cells fail explicitly. Valid B3 is user-controlled.
Preserve unrelated cells and avoid duplicate rules or formatting. Summary must
already exist; do not recreate it, add charts, or reconcile a full year.

---

# Email

Use Amazon SES for outbound daily emails.

The email should eventually offer:

- Full Day
- Half Day
- PTO
- Time Off

Do not require Gmail APIs for email delivery.

The Google API dependency is for Google Sheets only.

Email-response links must not mutate data through GET requests.

Email response date/status claims use versioned HMAC-SHA256 bearer tokens.
Token contents are signed, not encrypted, and do not authenticate human identity.
Tokens currently do not expire so old emails can support late responses and
corrections. The future handler must verify the token and use its signed claims
rather than trusting separate query parameters.

---

# Observability

Do not build custom logging, metrics, dashboards, alarms, tracing, or notification infrastructure at this stage.

AWS Lambda's normal CloudWatch output is sufficient during initial development.

Add observability only when there is a concrete operational reason.

---

# Expected Repository Shape

The project may evolve, but prefer approximately:

```text
cmd/
    admin-backfill/
        main.go

    admin-correct/
        main.go

    daily-worker/
        main.go

    status-handler/
        main.go

    sheet-sync/
        main.go

internal/
    tracker/
        day_record.go
        transitions.go

    store/
        dynamodb.go

    email/
        ses.go

    sheets/
        sheets.go

    token/
        token.go

    config/
        config.go

template.yaml
go.mod
go.sum
README.md
AGENTS.md
```

Do not create every package merely to satisfy this proposed tree. Create packages as functionality is actually implemented.

---

# Testing

Prioritize tests for business rules rather than trivial getters/setters.

Use idiomatic Go table-driven tests where appropriate.

Important behavior to test eventually includes:

- work and PTO fractions for each status;
- weekend detection;
- valid user state transitions;
- automatic `PENDING -> NO_RESPONSE`;
- normal versus late response source;
- repeated/idempotent status submission;
- correction of prior responses;
- daily-worker retry behavior where practical.

Avoid excessive mocking.

Pure domain logic should be testable without AWS.

AWS integration code should remain thin enough that most business behavior does not require AWS to test.

---

# Current Build Strategy

Build incrementally in this general order:

1. Domain model and state transitions.
2. DynamoDB storage layer.
3. Daily-worker application logic.
4. HTTP response handler.
5. Amazon SES integration.
6. Signed response tokens.
7. Google Sheets synchronization.
8. AWS SAM infrastructure.
9. End-to-end deployment/testing.
10. Spreadsheet presentation improvements.

## Project Decisions

Significant architectural or behavioral decisions that are not obvious
from the code should be recorded in `DECISIONS.MD`.

Keep this file concise. Do not log routine implementation choices or
turn it into a development diary.

When implementing a change that contradicts an existing decision,
raise the conflict before changing the code.

Do not skip ahead unless explicitly asked.
