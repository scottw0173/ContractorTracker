# ContractorTracker

ContractorTracker records daily contractor work status, PTO, time off, and
non-responses. DynamoDB is authoritative. A scheduled daily Lambda finalizes
an existing pending yesterday and sends today's SES prompt when needed.
Signed email links open a read-only confirmation page; an explicit POST
records the response through conditional DynamoDB updates. Google Sheets projection is driven by DynamoDB Stream notifications,
and is stream-only.

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

The public Function URL uses `AuthType: NONE`; signed bearer tokens provide
application authorization. The table is retained on stack deletion or
replacement, so retained data will require deliberate management later.

## Deployment

Stable non-sensitive SAM settings are committed in samconfig.toml: stack
contractor-tracker, region us-east-2, CAPABILITY_IAM, no change-set prompt, and
no failure on an empty change set. EmailFrom/EmailTo have no template defaults;
supply both externally. Never save email addresses, HMAC/Google credentials,
AWS credentials, account role ARNs, or parameter overrides in this file.

### One-time external setup

Using existing AWS administrator credentials in us-east-2, deploy the independent
bootstrap stack (these commands are instructions, not run by this repository):

```sh
aws cloudformation deploy \
  --region us-east-2 \
  --stack-name contractor-tracker-github-bootstrap \
  --template-file infra/github-actions-bootstrap.yaml \
  --capabilities CAPABILITY_NAMED_IAM
```

If this account already has the provider for
https://token.actions.githubusercontent.com, add
`--parameter-overrides ExistingGitHubOIDCProviderArn="$GITHUB_OIDC_PROVIDER_ARN"`
with its existing ARN. Reuse requires audience sts.amazonaws.com; do not create
a duplicate provider. Deploy the bootstrap only once in this account/region:
its role names are account-global and intentionally deterministic.

Record its outputs:

```sh
aws cloudformation describe-stacks --region us-east-2 \
  --stack-name contractor-tracker-github-bootstrap \
  --query 'Stacks[0].Outputs'
```

In GitHub, open Repository → Settings → Secrets and variables → Actions →
Variables and create:

- AWS_DEPLOY_ROLE_ARN: the GitHubDeployRoleArn output.
- EMAIL_FROM: the plain verified SES sending mailbox.
- EMAIL_TO: the intended recipient mailbox.

Keep the existing SSM secrets, SES identities, and Google spreadsheet access.
No external configuration is created automatically by this code. GitHub Variables
are non-secret and not log-masked. The workflow does not echo email values and
suppresses SAM output because SAM prints parameter overrides. GitHub may still
display environment configuration in workflow logs. To hide the email addresses,
store only EMAIL_FROM/EMAIL_TO as GitHub Secrets and change their two vars
references to secrets references; the application architecture stays the same.
Do not store AWS access keys in GitHub.

Bootstrap outputs also include CloudFormationExecutionRoleArn and ArtifactBucketName.
CI derives these from STS account ID and us-east-2 using the deterministic names;
no additional repository Variables are needed. The retained private, AES256-encrypted
artifact bucket stores deployment packages only, under contractor-tracker/.
It grants no access to application runtime roles. S3 account-level policies still
apply; retention means bootstrap deletion does not erase packaged artifacts.

### Routine deployment

Push/merge to main after setup. Pull requests run verification only, without AWS
authentication. A main push verifies formatting, tests, whitespace, SAM lint,
and all three binaries before deployment. The deploy job independently checks out,
sets up Go/SAM, assumes the GitHub role with OIDC, rebuilds, and deploys
noninteractively. No long-lived AWS credentials are stored in GitHub.
Deploy jobs are serialized without canceling an in-progress CloudFormation update.
Rollback remains enabled; documentation-only changes with no stack diff succeed.

Trust is restricted to audience sts.amazonaws.com and this exact immutable subject:

```text
repo:scottw0173@156988004/ContractorTracker@1403695477:ref:refs/heads/main
```

No GitHub environment is attached to the deploy job because it would change that
subject. Protect main and review workflow/bootstrap changes according to your
repository policy; this slice does not change repository settings.

The GitHub role can list/read/write only its artifact bucket/prefix, create/inspect/
execute/delete change sets for contractor-tracker, use the SAM transform, and pass
only contractor-tracker-cloudformation-execution to CloudFormation. It cannot
directly administer Lambda, DynamoDB, or application IAM roles.

The CloudFormation execution role manages the three app functions and their URL/
permissions, the SheetSync stream mapping, the retained day table/stream, the
default-group scheduler, and app-generated IAM roles/policies. Name/ARN patterns
preserve existing SAM physical names without forcing resource replacements.
IAM management cannot match bootstrap roles or the OIDC provider; managed-policy
attachment is limited to AWSLambdaBasicExecutionRole. PassRole is constrained
to app roles and Lambda/Scheduler. Artifact access is read-only.
Resource "*" is used only for lambda:CreateEventSourceMapping, which has no
resource-level authorization; lambda:FunctionArn still restricts it to SheetSync.
Other generated IDs use account/region-scoped patterns. New resource types or
renamed logical IDs may require a reviewed bootstrap policy update.

### Local deployment

With existing local AWS credentials, supply email values externally and use the
same noninteractive configuration. Do not use --guided or save overrides:

```sh
sam build --build-in-source
sam deploy \
  --s3-bucket "$ARTIFACT_BUCKET" \
  --s3-prefix contractor-tracker \
  --role-arn "$CLOUDFORMATION_EXECUTION_ROLE_ARN" \
  --parameter-overrides "EmailFrom=$EMAIL_FROM" "EmailTo=$EMAIL_TO"
```

Set the bucket/role from bootstrap outputs and both email variables locally.
Your identity needs the corresponding bucket/CloudFormation/PassRole access.
Alternatively use an existing private artifact bucket and omit --role-arn when
your manual deployment identity has resource-management permissions (an existing
stack service role can remain attached). SAM output may include email overrides;
keep local output private. Never supply the HMAC secret: CT-TokenSecret remains
the default SSM name, and schedule/timezone defaults remain production-ready.

## Automatic Sheets projection

`SheetSyncFunction` consumes INSERT/MODIFY notifications from DayTable's
KEYS_ONLY stream. It uses keys to strongly consistently reread current DynamoDB
state rather than projecting stream images. INSERT/MODIFY keys are validated
and deduplicated in first-seen order within each invocation before each unique
key runs GetDay -> UpsertDay. REMOVE notifications are ignored.
The Google client is initialized lazily for the first projection in each
invocation and reused within that batch. Empty/REMOVE-only batches do not load
Google credentials. It decrypts `GCP-Project-Key` from SSM, and upserts the
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
Shard ordering is not a global writer lock; concurrent shards or external writers
can race on tab creation or date-row append. Avoid concurrent external edits to
application-owned headers/date rows. Duplicate dates fail explicitly and require
cleanup. Enabling a stream does not backfill records that predate stream enablement.

The production DynamoDB Stream → SheetSync → Google Sheets path has been proven
end-to-end. There is no supported direct per-date SheetSync invocation.
Normal authoritative INSERT/MODIFY operations trigger projection automatically.
admin-backfill creates a missing DynamoDB day; admin-correct changes an existing
day; sheet-sync only projects stream notifications. Neither CLI calls SheetSync.
If an inconsistency cannot be repaired through a legitimate authoritative change,
a future explicit reconciliation/replay mechanism is required. No such mechanism
is implemented here; do not fabricate business changes solely to refresh Sheets.

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
and stream retry can finish dashboard initialization.

For safe deployment verification, inspect the change set to confirm the existing
DynamoDB trigger and IAM role remain intact and the manual function-name output
is removed. After deployment, observe a legitimate status submission or supported
administrative change. Verify its stream projection updates the same Daily Log
row and Summary formulas without duplicate rows or content. Do not submit custom
per-date Lambda payloads or create synthetic business changes for this test.

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
DynamoDB. Use `admin-correct` to change an existing day; SheetSync is an
automatic stream-only projection.
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
projects successful backfill INSERTs to Sheets.

Once each status is known, set STATUS_2026_10_01, STATUS_2026_10_02, and
STATUS_2026_10_04 to one of the four allowed values, then run each separately:

```sh
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-01 --status "$STATUS_2026_10_01"
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-02 --status "$STATUS_2026_10_02"
go run ./cmd/admin-backfill --table "$TABLE_NAME" --year 2026 --date 2026-10-04 --status "$STATUS_2026_10_04"
```

Review and confirm each preview independently. An unset status or table variable
fails validation before loading AWS configuration or making storage calls.
