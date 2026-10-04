# ContractorTracker

ContractorTracker records daily contractor work status, PTO, time off, and
non-responses. DynamoDB is authoritative. A scheduled daily Lambda finalizes
an existing pending yesterday and sends today's SES prompt when needed.
Signed email links open a read-only confirmation page; an explicit POST
records the response through conditional DynamoDB updates. Google Sheets
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

## Manual Sheets metadata check

`SheetSyncFunction` has no automatic trigger. On manual invocation it decrypts
`GCP-Project-Key` from SSM, authenticates as the service account, and reads only
spreadsheet metadata to require the existing `Daily Log` and `Summary` tabs.
The spreadsheet and parameter are configured through its environment variables
in `template.yaml`. Share the spreadsheet with the service account beforehand.
No cells, tabs, or formulas are changed; synchronization is still deferred.
If the SSM SecureString uses a customer-managed KMS key, its role will also need
scoped `kms:Decrypt` permission for that key before invocation.
