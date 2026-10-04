package contractortracker_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Locate the known mappings without adding a YAML dependency solely for tests.
// SAM lint independently validates YAML/CloudFormation syntax and semantics.
func templateBlock(t *testing.T, text string, path ...string) string {
	t.Helper()
	for depth, key := range path {
		prefix := strings.Repeat(" ", depth*2) + key + ":"
		lines := strings.Split(text, "\n")
		found := false
		for i, line := range lines {
			if line != prefix {
				continue
			}
			end := i + 1
			for end < len(lines) {
				next := lines[end]
				trimmed := strings.TrimSpace(next)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") && len(next)-len(strings.TrimLeft(next, " ")) <= depth*2 {
					break
				}
				end++
			}
			text = strings.Join(lines[i+1:end], "\n")
			found = true
			break
		}
		if !found {
			t.Fatalf("missing template mapping %s", strings.Join(path, "/"))
		}
	}
	return text
}
func readTemplate(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func requireTemplateText(t *testing.T, block string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(block, part) {
			t.Fatalf("missing template contract %s", part)
		}
	}
}

func TestTokenSecretParameterAndPermissions(t *testing.T) {
	text := readTemplate(t)
	if regexp.MustCompile(`(?m)^  TokenSecret:`).MatchString(templateBlock(t, text, "Parameters")) || regexp.MustCompile(`(?m)^\s+TOKEN_SECRET:`).MatchString(text) {
		t.Fatal("secret-value deployment parameter/environment remains")
	}
	parameter := templateBlock(t, text, "Parameters", "TokenSecretParameter")
	requireTemplateText(t, parameter, "Type: String", "Default: CT-TokenSecret")
	arn := `Resource: !Sub 'arn:${AWS::Partition}:ssm:${AWS::Region}:${AWS::AccountId}:parameter/${TokenSecretParameter}'`
	for _, name := range []string{"DailyWorkerFunction", "StatusHandlerFunction"} {
		t.Run(name, func(t *testing.T) {
			function := templateBlock(t, text, "Resources", name)
			environment := templateBlock(t, text, "Resources", name, "Properties", "Environment", "Variables")
			requireTemplateText(t, environment, "TOKEN_SECRET_PARAMETER: !Ref TokenSecretParameter")
			statements := strings.Split(function, "- Effect: Allow")
			count := 0
			for _, statement := range statements {
				if !strings.Contains(statement, "ssm:") {
					continue
				}
				count++
				requireTemplateText(t, statement, "Action: ssm:GetParameter", arn)
				if strings.Contains(statement, "ssm:*") || strings.Contains(statement, "kms:") {
					t.Fatal("broad SSM or unnecessary KMS permissions")
				}
			}
			if count != 1 {
				t.Fatal("expected exactly one scoped SSM statement")
			}
		})
	}
	// SheetSync credential access and its separate role remain intact.
	requireTemplateText(t, templateBlock(t, text, "Resources", "SheetSyncFunctionRole"), "Action: ssm:GetParameter", ":parameter/GCP-Project-Key'")
}

func TestProductionScheduleDefaults(t *testing.T) {
	text := readTemplate(t)
	requireTemplateText(t, templateBlock(t, text, "Parameters", "DailyScheduleExpression"), "Default: cron(0 6 * * ? *)")
	requireTemplateText(t, templateBlock(t, text, "Parameters", "DailyScheduleState"), "Default: ENABLED", "- ENABLED", "- DISABLED")
	requireTemplateText(t, templateBlock(t, text, "Parameters", "AppTimezone"), "Default: America/Mazatlan")
	requireTemplateText(t, templateBlock(t, text, "Resources", "DailyWorkerFunction", "Properties", "Events", "DailySchedule"), "Type: ScheduleV2", "ScheduleExpression: !Ref DailyScheduleExpression", "ScheduleExpressionTimezone: !Ref AppTimezone", "State: !Ref DailyScheduleState")
}
