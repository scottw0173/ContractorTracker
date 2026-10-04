# ContractorTracker

ContractorTracker records daily contractor work status, PTO, time off, and
non-responses. DynamoDB is authoritative. A scheduled daily Lambda finalizes
an existing pending yesterday and sends today's SES prompt when needed.
Signed email links open a read-only confirmation page; an explicit POST
records the response through conditional DynamoDB updates. Google Sheets projection is driven by DynamoDB Stream notifications,
with manual per-date repair available.

## Build and validate

Prerequisites: Go 1.25.5 or newer, Make, AWS SAM CLI, and AWS CLI credentials
for deployment. The sending mailbox must be a verified SES
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

## Production configuration

The full production path has been successfully tested end-to-end. There are
three Lambda binaries plus the local admin-correct/admin-backfill CLIs.
Use a plain SES-verified EmailFrom mailbox in the stack region and preserve the
configured production EmailTo recipient. Delivery to a real recipient still
requires verification while SES is in the sandbox unless production access has
been granted.

The production schedule defaults to `cron(0 6 * * ? *)`, timezone
`America/Mazatlan`, and state `ENABLED`: every day at 6:00 AM application local
time. DailyScheduleExpression, AppTimezone, and DailyScheduleState remain
parameter overrides.

TokenSecretParameter defaults to `CT-TokenSecret`, an existing SSM parameter
expected to be SecureString in the stack's region. Each daily-worker and
status-handler cold start retrieves it once with decryption, using the execution
role. Their environments contain only TOKEN_SECRET_PARAMETER; they do not read
TOKEN_SECRET. The secret value is not a CloudFormation deployment parameter and
must not be supplied to sam deploy, committed, printed, or logged.

Preserve the existing secret byte-for-byte, including any whitespace, so old
signed links remain valid. This changes retrieval only, not HMAC, token format,
or expiration. Warm execution environments retain the cold-start secret; changing
it is not a coordinated rotation mechanism. Production expects normal SSM-managed
SecureString encryption. A future customer-managed KMS key would need narrowly
scoped kms:Decrypt permissions and its key policy; no broad KMS access is added.

For the next deployment (not performed in this slice):

1. Verify CT-TokenSecret exists as SecureString in the deployment region without
   displaying its value. Keep the same existing production secret.
2. Remove the old TokenSecret override from deployment commands and private
   samconfig.toml. Keep secret material and samconfig.toml out of Git.
3. Preserve existing stack/mailbox settings and explicitly supply
   TokenSecretParameter=CT-TokenSecret, DailyScheduleExpression="cron(0 6 * * ? *)",
   AppTimezone=America/Mazatlan, and DailyScheduleState=ENABLED. Existing stack or
   guided-deployment overrides can otherwise preserve old disabled/time values.
4. Review the change set for removal of TOKEN_SECRET, addition of the name-only
   environment setting, and each function's scoped SSM GetParameter permission.
5. After deployment, open an old email link to GET its confirmation page without
   submitting it. This safely checks cold-start retrieval and unchanged signing
   compatibility. Check startup logs without printing SDK causes or secret values.
6. To manually smoke-test the worker, first verify today's record has already
   responded or been emailed if you want to avoid another prompt. Its normal
   finalization/send behavior is unchanged. Verify the deployed schedule state,
   timezone, and expression, then observe the next 6:00 AM run.

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
Presentation is not business state; Summary has its own dashboard presentation.

Blank headers are initialized; nonblank mismatches and duplicate dates fail.
Repeated invocation updates the same row. Unrelated Daily Log rows are not
rewritten; there is no sorting or full-year reconciliation. Successful upserts
also maintain the Summary dashboard described below.
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

## Summary dashboard

Summary shows one selected calendar year. B3 is a strict dropdown of existing
`Daily Log YYYY` tabs, sorted ascending. Blank/invalid selections default to
the newest year. Tab creation, year selection, and its dropdown update share
one atomic Google batch, so a later projection failure cannot lose the once-only
year switch. Ordinary later upserts preserve a valid manually selected past year. No DynamoDB queries are
performed for Summary calculations.

The application-owned layout is:

| Cells | Content |
| --- | --- |
| A1 | Contractor Summary title |
| A3:B3 | Reporting Year label and dropdown |
| D3:E3 | Records Through label and latest logical date |
| A4:B4 | Today's Status label and current-year-only status |
| A6:F7 | Workday Equivalent, Full Days, Half Days, PTO Used, No Responses, Corrections |
| A10 | Monthly Breakdown title |
| A11:G24 | Month, Work Eq., Full, Half, PTO, Time Off, No Response; January–December and TOTAL |
| A27, D27 | PTO Days title and visible overflow warning when necessary |
| A28:B43 | Date/Day headers and 15 sorted PTO date rows |

All totals describe current record state. A late Full Day contributes work and
Full Days, not No Responses; historical response/finalization metadata is not
aggregated. All NO_RESPONSE statuses count equally, including weekends. Work
Eq. sums Work Fraction, PTO Used sums PTO Fraction with no assumed entitlement
or remaining allowance, and Corrections counts Changed=true as descriptive
application metadata rather than an audit log. The monthly breakdown replaces
weekly averages. ISO month-prefix formulas operate on RAW text dates.

The PTO list uses current PTO records in chronological order, with the expected
annual 15-day capacity as display space only. More than 15 records produces a
visible warning directing the viewer to the selected Daily Log for all dates;
underlying records and PTO Used are never truncated. Records Through uses the
maximum logical date, independent of row order. Today's Status uses Sheets
TODAY(); verify the spreadsheet timezone matches the application's timezone.

Only blank required cells are initialized. Compatible formulas/labels remain
unchanged, valid B3 selections are user-controlled, and incompatible nonblank
content returns an error naming its cell. Unrelated cells are not overwritten.
Formatting and year validation use narrow, repeat-safe updates. Summary updates
follow a successful Daily Log write: a Summary failure does not undo that row,
and manual invocation or stream retry can finish dashboard initialization.

For a safe dashboard regression test, save a copy of the existing workbook and
inspect the owned cells above for incompatible content before deploying. Invoke SheetSync
with an existing 2026 DynamoDB date using the command above. Confirm no
FunctionError, B3=2026, correct formula results (no formula errors), current-date
status, monthly totals, and sorted PTO dates. Compare headline sums/counts with
Daily Log 2026, including weekend and late-response records if present. Invoke
the same date again to verify no duplicate Daily Log row or dashboard content.
If another annual tab already exists, select 2026 and repeat an ordinary upsert
for the other year to verify the selection remains 2026. No synthetic business
records are needed for this test.

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

## Creating missing historical days

Use `cmd/admin-backfill` to create a day that is missing from authoritative
DynamoDB. Use `admin-correct` to change an existing day; manual SheetSync
invocation only repairs a projection and does not create authoritative records.
Direct manual construction/editing of DynamoDB business fields is unsupported.

```sh
go run ./cmd/admin-backfill \
  --table "$TABLE_NAME" \
  --year 2026 \
  --date 2026-10-01 \
  --status FULL_DAY
```

Set TABLE_NAME to the deployed table name and choose the known status from
FULL_DAY, HALF_DAY, PTO, or TIME_OFF. All four flags are required; positional
arguments are rejected. The command uses the standard AWS SDK v2 credential
chain, requiring the operator's existing table GetItem and PutItem permissions.
No new SAM/IAM resource, token, Google credential, or direct Sheets call is used.

The CLI strongly reads the day, previews Date, Status, Work Fraction, PTO Fraction,
Weekend, Response Source, and Responded At, then asks
`Create this historical record? [y/N]`. Only exact y/yes (case-insensitive)
confirms; every other answer cancels without writing. An existing record blocks
creation and directs you to admin-correct. If another actor creates the item
before confirmation completes, conditional CreateDay reports that it now exists
without overwriting it. There is no retry that converts creation into correction.

The pure domain helper sets normal status/fraction values and calendar weekend
context, source ADMIN_BACKFILL, HasBeenChanged=false, and the current UTC
administrative entry time in RespondedAt. EmailSentAt and FinalizedAt remain
zero; no historical email-response time is invented. Different-status corrections
later set HasBeenChanged and use normal response-source rules; a same-status
admin-correct request remains a no-op. The existing DynamoDB Stream automatically
projects successful backfill INSERTs to Sheets. Manual SheetSync repair remains
available separately.

Once each status is known, set STATUS_2026_10_01, STATUS_2026_10_02, and
STATUS_2026_10_04 to one of the four allowed values, then run each separately:

```sh
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-01 --status "$STATUS_2026_10_01"
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-02 --status "$STATUS_2026_10_02"
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-04 --status "$STATUS_2026_10_04"
```

Review and confirm each preview independently. An unset status or table variable
fails validation before loading AWS configuration or making storage calls.
