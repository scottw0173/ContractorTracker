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

func TestWorkflowImmutableActionPins(t *testing.T) {
	text := readDeploymentFile(t, ".github/workflows/ci-deploy.yml")
	approved := map[string]struct {
		sha, version string
		count        int
	}{
		"actions/checkout":                      {"d23441a48e516b6c34aea4fa41551a30e30af803", "v6", 2},
		"actions/setup-go":                      {"924ae3a1cded613372ab5595356fb5720e22ba16", "v6", 2},
		"aws-actions/setup-sam":                 {"89ddb14d60e682855e3fea4be85b3c56485de310", "v3", 2},
		"aws-actions/configure-aws-credentials": {"e1253824e5c10ff9df46874f81ed3ec929e19cfd", "v6.3.0", 1},
	}
	uses := regexp.MustCompile(`(?m)^\s*- uses:\s+([^@\s]+)@([^\s#]+)\s+# ([^\s]+)\s*$`).FindAllStringSubmatch(text, -1)
	if len(uses) != strings.Count(text, "uses:") {
		t.Fatal("every Action reference must include an immutable pin and version comment")
	}
	counts := make(map[string]int)
	for _, use := range uses {
		pin, ok := approved[use[1]]
		if !ok || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(use[2]) || use[2] != pin.sha || use[3] != pin.version {
			t.Fatalf("unapproved Action reference: %s", use[0])
		}
		counts[use[1]]++
	}
	for action, pin := range approved {
		if counts[action] != pin.count || strings.Contains(text, action+"@"+pin.version) {
			t.Errorf("%s must use its approved SHA in every expected occurrence", action)
		}
	}
	// TestWorkflowCredentialBoundary independently enforces that verify has no
	// AWS access and only verified main pushes can request deployment credentials.
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
	for _, forbidden := range []string{"id-token:", "configure-aws-credentials", "vars.EMAIL", "secrets.EMAIL", "role-to-assume", "sam deploy"} {
		if strings.Contains(verify, forbidden) {
			t.Fatalf("verify has credential/deployment access: %s", forbidden)
		}
	}
	requireTemplateText(t, verify, "go-version-file: go.mod", "gofmt -l", "go test ./...", "git diff --check", "sam validate --lint", "sam build --build-in-source")
	requireTemplateText(t, deploy, "needs: verify", "if: github.event_name == 'push' && github.ref == 'refs/heads/main'", "id-token: write", "contents: read", "vars.AWS_DEPLOY_ROLE_ARN", "secrets.EMAIL_FROM", "secrets.EMAIL_TO", "aws-actions/configure-aws-credentials@", "aws sts get-caller-identity", "sam build --build-in-source", "--s3-bucket", "--role-arn", "--no-confirm-changeset", "--no-fail-on-empty-changeset", `"EmailFrom=$EMAIL_FROM" "EmailTo=$EMAIL_TO"`, "Missing repository variable AWS_DEPLOY_ROLE_ARN", "Missing repository secret %s")
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

func TestExecutionRoleSAMTransformPermission(t *testing.T) {
	text := readDeploymentFile(t, "infra/github-actions-bootstrap.yaml")
	execution := templateBlock(t, text, "Resources", "CloudFormationExecutionRole")
	parts := strings.Split(execution, "- Sid: AllowSAMTransform")
	if len(parts) != 2 {
		t.Fatal("execution role requires exactly one SAM transform statement")
	}
	statement := strings.SplitN(parts[1], "- Sid:", 2)[0]
	requireTemplateText(t, statement, "Effect: Allow", "Action: cloudformation:CreateChangeSet",
		"Resource: !Sub 'arn:${AWS::Partition}:cloudformation:${AWS::Region}:aws:transform/Serverless-2016-10-31'")
	if strings.Contains(statement, "*") || strings.Count(statement, "Action:") != 1 || strings.Count(statement, "Resource:") != 1 {
		t.Fatal("SAM transform grant must be narrowly scoped")
	}
	// No other CloudFormation administration is authorized in this execution role.
	if strings.Count(execution, "Action: cloudformation:") != 1 {
		t.Fatal("unexpected execution-role CloudFormation permissions")
	}
}

func TestWorkflowStepSecretsAndPrivateValidation(t *testing.T) {
	text := readDeploymentFile(t, ".github/workflows/ci-deploy.yml")
	deploy := templateBlock(t, text, "jobs", "deploy")
	env := templateBlock(t, text, "jobs", "deploy", "env")
	if strings.Contains(env, "EMAIL_") || strings.Contains(text, "vars.EMAIL_FROM") || strings.Contains(text, "vars.EMAIL_TO") {
		t.Fatal("email values must not be job-level env or Variables")
	}
	requireTemplateText(t, env, "vars.AWS_DEPLOY_ROLE_ARN")
	for _, name := range []string{"Validate repository configuration", "Deploy"} {
		parts := strings.SplitN(deploy, "- name: "+name+"\n", 2)
		if len(parts) != 2 {
			t.Fatalf("missing step %s", name)
		}
		step := strings.SplitN(parts[1], "      - ", 2)[0]
		requireTemplateText(t, step, "env:", "EMAIL_FROM: ${{ secrets.EMAIL_FROM }}", "EMAIL_TO: ${{ secrets.EMAIL_TO }}")
	}
	// Execute the actual validation run block offline, using opaque fixture values.
	start := strings.Index(deploy, "- name: Validate repository configuration")
	block := strings.SplitN(deploy[start:], "run: |\n", 2)[1]
	block = strings.SplitN(block, "      - ", 2)[0]
	var script strings.Builder
	for _, line := range strings.Split(block, "\n") {
		script.WriteString(strings.TrimPrefix(line, "          "))
		script.WriteByte('\n')
	}
	fixtures := map[string]string{
		"AWS_DEPLOY_ROLE_ARN": "role-fixture",
		"EMAIL_FROM":          "private-from-fixture",
		"EMAIL_TO":            "private-to-fixture",
	}
	for _, missing := range []string{"", "AWS_DEPLOY_ROLE_ARN", "EMAIL_FROM", "EMAIL_TO"} {
		for _, blank := range []string{"", " \t\n"} {
			cmd := exec.Command("bash", "-e", "-c", script.String())
			cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
			for name, value := range fixtures {
				if name == missing {
					value = blank
				}
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if strings.Contains(string(output), fixtures["EMAIL_FROM"]) || strings.Contains(string(output), fixtures["EMAIL_TO"]) {
				t.Fatal("validation leaked fixture secret")
			}
			if missing == "" {
				if err != nil || len(output) != 0 {
					t.Fatalf("valid configuration failed: %s (%v)", output, err)
				}
				continue
			}
			kind := "secret"
			if missing == "AWS_DEPLOY_ROLE_ARN" {
				kind = "variable"
			}
			want := "::error::Missing repository " + kind + " " + missing + "\n"
			if err == nil || string(output) != want {
				t.Fatalf("missing %s: output=%q error=%v", missing, output, err)
			}
		}
	}
}
