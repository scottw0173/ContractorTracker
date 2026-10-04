package contractortracker_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func readDeploymentFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExternalEmailConfiguration(t *testing.T) {
	text := readTemplate(t)
	for _, name := range []string{"EmailFrom", "EmailTo"} {
		block := templateBlock(t, text, "Parameters", name)
		if strings.Contains(block, "Default:") {
			t.Fatalf("%s must be externally supplied", name)
		}
		requireTemplateText(t, block, "Type: String", "MinLength: 3", "AllowedPattern:")
	}
	// Template may contain parameter references and validation patterns, never mailboxes.
	if regexp.MustCompile(`[A-Za-z0-9_.+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`).MatchString(text) {
		t.Fatal("email address committed in application template")
	}
}

func TestSafeSAMConfig(t *testing.T) {
	config := readDeploymentFile(t, "samconfig.toml")
	for _, forbidden := range []string{"parameter_overrides", "EmailFrom", "EmailTo", "TokenSecret", "GOOGLE", "role_arn", "s3_bucket", "access_key", "secret_key", "arn:"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("private/account config %s in samconfig", forbidden)
		}
	}
	requireTemplateText(t, config, `stack_name = "contractor-tracker"`, `region = "us-east-2"`, `capabilities = "CAPABILITY_IAM"`, "confirm_changeset = false", "fail_on_empty_changeset = false")
	if out, err := exec.Command("git", "check-ignore", "samconfig.toml").CombinedOutput(); err == nil || len(out) != 0 {
		t.Fatalf("samconfig must not be ignored: %s (%v)", out, err)
	}
}

func TestWorkflowCredentialBoundary(t *testing.T) {
	text := readDeploymentFile(t, ".github/workflows/ci-deploy.yml")
	requireTemplateText(t, text, "pull_request:", "branches: [main]")
	if strings.Contains(text, "pull_request_target") {
		t.Fatal("unsafe PR trigger")
	}
	verify := templateBlock(t, text, "jobs", "verify")
	deploy := templateBlock(t, text, "jobs", "deploy")
	for _, forbidden := range []string{"id-token:", "configure-aws-credentials", "vars.EMAIL", "role-to-assume", "sam deploy"} {
		if strings.Contains(verify, forbidden) {
			t.Fatalf("verify has credential/deployment access: %s", forbidden)
		}
	}
	requireTemplateText(t, verify, "go-version-file: go.mod", "gofmt -l", "go test ./...", "git diff --check", "sam validate --lint", "sam build --build-in-source")
	requireTemplateText(t, deploy, "needs: verify", "if: github.event_name == 'push' && github.ref == 'refs/heads/main'", "id-token: write", "contents: read", "vars.AWS_DEPLOY_ROLE_ARN", "vars.EMAIL_FROM", "vars.EMAIL_TO", "aws-actions/configure-aws-credentials@", "aws sts get-caller-identity", "sam build --build-in-source", "--s3-bucket", "--role-arn", "--no-confirm-changeset", "--no-fail-on-empty-changeset", `"EmailFrom=$EMAIL_FROM" "EmailTo=$EMAIL_TO"`, "Missing repository variable %s")
	if strings.Count(text, "id-token: write") != 1 {
		t.Fatal("OIDC permission must be deploy-only")
	}
	if strings.Index(deploy, "Validate repository configuration") > strings.Index(deploy, "aws-actions/configure-aws-credentials@") {
		t.Fatal("missing configuration must fail before authentication")
	}
	for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "--guided", "--resolve-s3", "--disable-rollback", "TokenSecret=", "AppTimezone=", "DailyScheduleExpression=", "DailyScheduleState="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsupported deployment setting %s", forbidden)
		}
	}
	if strings.Index(deploy, "sam build --build-in-source") > strings.Index(deploy, "sam deploy") {
		t.Fatal("deploy must independently build first")
	}
}

func TestBootstrapTrustAndSeparation(t *testing.T) {
	text := readDeploymentFile(t, "infra/github-actions-bootstrap.yaml")
	if regexp.MustCompile(`(?m):\s+[&*][A-Za-z]`).MatchString(text) {
		t.Fatal("CloudFormation template must not rely on YAML anchors/aliases")
	}
	trust := templateBlock(t, text, "Resources", "GitHubDeployRole", "Properties", "AssumeRolePolicyDocument")
	requireTemplateText(t, trust, "sts:AssumeRoleWithWebIdentity", "StringEquals:", "token.actions.githubusercontent.com:aud: sts.amazonaws.com", "token.actions.githubusercontent.com:sub: repo:scottw0173@156988004/ContractorTracker@1403695477:ref:refs/heads/main", "!Ref ExistingGitHubOIDCProviderArn", "!Ref GitHubOIDCProvider")
	if strings.Contains(trust, "*") || strings.Contains(trust, "pull_request") || strings.Contains(trust, "StringLike") {
		t.Fatal("OIDC trust must be exact immutable main subject")
	}
	deploy := templateBlock(t, text, "Resources", "GitHubDeployRole", "Properties", "Policies")
	for _, forbidden := range []string{"lambda:", "dynamodb:", "iam:Create", "AdministratorAccess"} {
		if strings.Contains(deploy, forbidden) {
			t.Fatalf("OIDC deploy principal has resource administration: %s", forbidden)
		}
	}
	requireTemplateText(t, deploy, "stack/contractor-tracker/*", "iam:PassRole", "Resource: !GetAtt CloudFormationExecutionRole.Arn", "iam:PassedToService: cloudformation.amazonaws.com")
	execution := templateBlock(t, text, "Resources", "CloudFormationExecutionRole")
	if strings.Contains(execution, "oidc-provider") || strings.Contains(execution, "GitHubDeployRole") || strings.Contains(execution, "AdministratorAccess") {
		t.Fatal("execution role must not administer bootstrap")
	}
	// The only unscoped resource statement is a Lambda API without resource-level auth.
	if strings.Count(execution, "Resource: '*'") != 1 {
		t.Fatal("unexpected unscoped execution permissions")
	}
	requireTemplateText(t, execution, "Action: lambda:CreateEventSourceMapping", "lambda:FunctionArn:", "role/contractor-tracker-DailyWorkerFunctionRole-*", "role/contractor-tracker-SheetSyncFunctionRole-*", "iam:PassedToService: [lambda.amazonaws.com, scheduler.amazonaws.com]")
	bucket := templateBlock(t, text, "Resources", "ArtifactBucket")
	requireTemplateText(t, bucket, "BlockPublicAcls: true", "IgnorePublicAcls: true", "BlockPublicPolicy: true", "RestrictPublicBuckets: true", "SSEAlgorithm: AES256")
	outputs := templateBlock(t, text, "Outputs")
	requireTemplateText(t, outputs, "GitHubDeployRoleArn:", "CloudFormationExecutionRoleArn:", "ArtifactBucketName:")
}
