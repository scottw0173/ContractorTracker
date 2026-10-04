# ContractorTracker

ContractorTracker records daily contractor work status, PTO, time off, and
non-responses. DynamoDB is authoritative. A scheduled daily Lambda finalizes
an existing pending yesterday and sends today's SES prompt when needed.
Signed email links open a read-only confirmation page; an explicit POST
records the response through conditional DynamoDB updates. Google Sheets projection is driven by DynamoDB Stream notifications,
with manual per-date repair available.

## Build and validate

Prerequisites: Go 1.25.5 or newer, Make, AWS SAM CLI, and AWS CLI credentials
for the eventual deployment. The sending mailbox must be a verified SES
identity in the same AWS region where the stack will be deployed.

```sh
go test ./...
sam validate --lint
sam build
```

SAM uses the root Makefile to build three Linux amd64 binaries named
`bootstrap`, using `lambda.norpc`, for ZIP deployment on `provided.al2023`.
No Docker build is required. Configuration is read at cold start; execution
roles and the standard AWS SDK configuration chain supply AWS settings.

## First deployment settings

No AWS deployment has been performed as part of implementation. For the
first deployment, supply the required `EmailFrom`, `TokenSecret`, and
`DailyScheduleExpression` parameters. Use a plain SES-verified sending
mailbox without a display name. Choose the actual schedule time yourself;
`cron(0 9 * * ? *)` is only an example, not the project's chosen time.
`AppTimezone` defaults to `America/Mazatlan` for both calendar dates and
schedule evaluation.

`EmailTo` defaults to `success@simulator.amazonses.com`. `DailyScheduleState`
defaults to `DISABLED`; leave it disabled until manual end-to-end testing is
complete. Delivery to a real recipient requires recipient verification while
the SES account is in the sandbox, unless it has production access.

Generate a strong random token secret locally with a cryptographically secure
tool, for example `openssl rand -hex 32`, and supply it privately at deployment.
Do not commit the secret or `samconfig.toml`; guided SAM deployment may save
parameter overrides there. The parameter is `NoEcho`, but the secret is still
in both Lambda environments and must be protected through AWS access control.
This environment-based secret storage is a deliberate choice for this personal
application rather than a general recommendation.

The public Function URL uses `AuthType: NONE`; signed bearer tokens provide
application authorization. The table is retained on stack deletion or
replacement, so retained data will require deliberate management later.

## Sheets projection and manual repair

`SheetSyncFunction` consumes INSERT/MODIFY notifications from DayTable's
KEYS_ONLY stream. It uses keys to strongly consistently reread current DynamoDB
state rather than projecting stream images. INSERT/MODIFY keys are validated
and deduplicated in first-seen order within each invocation before each unique
key runs GetDay -> UpsertDay. REMOVE notifications are ignored.
Manual `{year, date}` events remain available for repair/backfill. It decrypts `GCP-Project-Key` from SSM, and upserts the
record into `Daily Log YYYY`, creating the yearly tab if needed. `Summary` must
already exist. The unyearly `Daily Log` tab is unused. Share the spreadsheet with
the service account with edit access beforehand. A customer-managed SSM KMS key
also requires scoped `kms:Decrypt` permission.

Header order is exactly:

```text
Date | Day | Status | Work Fraction | PTO Fraction | Weekend | Email Sent At | Responded At | Response Source | Finalized At | Changed
```

Yearly Daily Log presentation freezes row 1, preserves existing banding that
covers A1:K370 (including its colors), and auto-sizes A:K after each successful
row upsert. Missing banding is added with subdued defaults; yearly color schemes
need not match. Partial overlapping bands that do not cover A1:K370 require
manual adjustment and return an error instead of adding overlapping formatting.
Presentation is not business state, and Summary formatting is untouched.

Blank headers are initialized; nonblank mismatches and duplicate dates fail.
Repeated invocation updates the same row. Unrelated rows and Summary are not
rewritten; there is no sorting, full-year reconciliation, or Summary formula setup.
DynamoDB remains authoritative. The stream starts at TRIM_HORIZON with batch
size 10 and default per-shard parallelization. An error fails the whole batch
for default Lambda retry; there is no partial-batch response or custom retry.
The function uses an explicit role with narrowly scoped inline stream-read
permissions, separate table GetItem permission, and no managed DynamoDB policy.

Reserved concurrency is absent because the account rejected its reservation.
Shard ordering is not a global writer lock; concurrent shards/manual invocations
can race on tab creation or date-row append. Avoid manual invocations during
active stream processing and concurrent external edits to application-owned
headers/date rows. Duplicate dates fail explicitly and require manual cleanup.
Enabling a stream does not backfill records that predate stream enablement;
manual invocation remains available for those records.

After deployment, choose a date that already exists in DynamoDB. For example:

```json
{"year":2026,"date":"2026-10-03"}
```

Use the deployed `SheetSyncFunctionName` output as the function name (replace
the placeholder below). Use AWS CLI credentials and region for that stack:

```sh
aws lambda invoke \
  --function-name '<SheetSyncFunctionName output>' \
  --invocation-type RequestResponse \
  --cli-binary-format raw-in-base64-out \
  --payload '{"year":2026,"date":"2026-10-03"}' \
  /tmp/contractortracker-sheet-sync-result.json
cat /tmp/contractortracker-sheet-sync-result.json
```

Success returns `null`; check that the CLI response has no `FunctionError`.
Invoking again should update the same date row rather than append another.

## Administrative corrections

Use `cmd/admin-correct` for administrative business-state corrections rather
than direct DynamoDB console field edits. Console edits bypass application
transition semantics and are not a supported correction mechanism.

```sh
go run ./cmd/admin-correct \
  --table '<deployed DayTable name>' \
  --year 2026 \
  --date 2026-10-04 \
  --status FULL_DAY
```

Targets are `FULL_DAY`, `HALF_DAY`, `PTO`, or `TIME_OFF`. Existing local AWS
credentials and region use the SDK default configuration chain; the operator's
identity must have table `GetItem` and `UpdateItem` access. No token or service
account credential is needed by this CLI.

The command previews the transition and asks `Apply this correction? [y/N]`.
Only `y` or `yes`, case-insensitive, permits a write. It uses the same domain
transition function and narrow conditional update as normal responses, with
at most three read/apply/write attempts. A conflict causes a fresh preview and
another confirmation. Same-status requests report no effective change and
perform no write. Missing records are errors; this command does not create days.

Initial responses preserve the correction flag and use `USER`; responses after
`NO_RESPONSE` use `LATE_USER` and preserve `FinalizedAt`. Changes between user
statuses permanently set `HasBeenChanged`; late corrections remain `LATE_USER`.
`HasBeenChanged` is application state, not a tamper-proof audit log. Response
timestamps come from current UTC time at the CLI boundary, with no timestamp
flag. Successful writes naturally wake the existing Sheets stream projection;
the CLI never calls Sheets and needs no Lambda or SAM infrastructure.
