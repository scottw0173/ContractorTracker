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

The integration boundary is cmd/daily-worker and cmd/status-handler. It loads
application environment settings, then the AWS SDK v2 default configuration,
and constructs the existing services once at cold start. Business packages
continue to receive explicit timestamps and injected clients/dependencies.

SAM uses provided.al2023, bootstrap, and x86_64 ZIP deployment with root
Makefile targets for both functions. ScheduleV2 uses the application timezone
and is initially disabled. The Function URL uses AuthType: NONE with signed
bearer tokens as application authorization. The DynamoDB table is retained
on stack deletion/replacement. The initial email recipient is the SES simulator.
Secrets and generated/local deployment files must not be committed.

---

# High-Level Architecture

There are two Lambda functions.

## daily-worker

Triggered once per day by EventBridge Scheduler.

Responsibilities:

1. Determine today's and yesterday's dates using the configured application timezone.
2. Load yesterday's record.
3. If yesterday exists and is still `PENDING`, convert it to `NO_RESPONSE`
   using a conditional write requiring the stored status still to be `PENDING`.
   Leave missing records absent and continue after conditional conflicts.
4. Eventually synchronize DynamoDB records to Google Sheets (not yet implemented).
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
```

Expected meanings:

`USER`
: A user response before the day's record had been automatically finalized as `NO_RESPONSE`.

`LATE_USER`
: A user response after the record had previously been automatically finalized as `NO_RESPONSE`.

`AUTO_FINALIZE`
: The daily worker found a previous `PENDING` record and automatically changed it to `NO_RESPONSE`.

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

The reporting layer can interpret weekend and weekday non-responses differently later.

---

# Google Sheets

Google Sheets is a reporting projection, not the database.

internal/sheets provides credential loading and client construction only.
GOOGLE_CREDENTIALS_PARAMETER and GOOGLE_SPREADSHEET_ID select the SSM parameter
and existing target. Credentials are decrypted from Parameter Store and used
for service-account authentication with the Sheets read/write scope, without
Drive access or default credential discovery. No spreadsheet synchronization,
worksheet setup, or Lambda integration is implemented by this plumbing.

DynamoDB must remain authoritative.

Initial intent:

- Read the relevant DynamoDB records.
- Sort them chronologically.
- Synchronize the worksheet deterministically.

Avoid designing the system around blind spreadsheet row appends.

A later spreadsheet may contain:

- a raw daily-record view;
- a simplified time-off view;
- formulas or summaries.

Do not build advanced spreadsheet reporting until explicitly requested.

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
    daily-worker/
        main.go

    status-handler/
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