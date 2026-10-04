# ContractorTracker

ContractorTracker records daily contractor work status, PTO, time off, and
non-responses. DynamoDB is authoritative. A scheduled daily Lambda finalizes
an existing pending yesterday and sends today's SES prompt when needed.
Signed email links open a read-only confirmation page; an explicit POST
records the response through conditional DynamoDB updates. Manual per-date Google Sheets projection is available; automatic
synchronization is not implemented yet.

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

## Manual Sheets projection

`SheetSyncFunction` accepts one `{year, date}` event and has no automatic trigger.
It reads that DynamoDB day, decrypts `GCP-Project-Key` from SSM, and upserts the
record into `Daily Log YYYY`, creating the yearly tab if needed. `Summary` must
already exist. The unyearly `Daily Log` tab is unused. Share the spreadsheet with
the service account with edit access beforehand. A customer-managed SSM KMS key
also requires scoped `kms:Decrypt` permission.

Header order is exactly:

```text
Date | Day | Status | Work Fraction | PTO Fraction | Weekend | Email Sent At | Responded At | Response Source | Finalized At | Changed
```

Blank headers are initialized; nonblank mismatches and duplicate dates fail.
Repeated invocation updates the same row. Unrelated rows and Summary are not
rewritten; there is no sorting, full-year reconciliation, or Stream trigger.
DynamoDB remains authoritative. Reserved concurrency is one; avoid simultaneous
external edits to application-owned headers and date rows.

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
