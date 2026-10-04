// Package config loads application settings only. AWS SDK configuration belongs
// to the Lambda entrypoints, and URL/mailbox validation belongs to email.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // ZIP deployments must not depend on runtime zoneinfo files.
)

type Daily struct {
	TableName       string
	TokenSecret     []byte
	Location        *time.Location
	ResponseBaseURL string
	EmailFrom       string
	EmailTo         string
}

type Status struct {
	TableName   string
	TokenSecret []byte
}

func required(name string) (string, error) {
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be missing or blank", name)
	}
	// Preserve the original value, especially secrets; only blankness is checked.
	return value, nil
}

func LoadStatus() (Status, error) {
	table, err := required("TABLE_NAME")
	if err != nil {
		return Status{}, err
	}
	secret, err := required("TOKEN_SECRET")
	if err != nil {
		return Status{}, err
	}
	return Status{TableName: table, TokenSecret: []byte(secret)}, nil
}

func LoadDaily() (Daily, error) {
	status, err := LoadStatus()
	if err != nil {
		return Daily{}, err
	}
	timezone, err := required("APP_TIMEZONE")
	if err != nil {
		return Daily{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Daily{}, fmt.Errorf("APP_TIMEZONE: %w", err)
	}
	base, err := required("RESPONSE_BASE_URL")
	if err != nil {
		return Daily{}, err
	}
	from, err := required("EMAIL_FROM")
	if err != nil {
		return Daily{}, err
	}
	to, err := required("EMAIL_TO")
	if err != nil {
		return Daily{}, err
	}
	return Daily{TableName: status.TableName, TokenSecret: status.TokenSecret, Location: location, ResponseBaseURL: base, EmailFrom: from, EmailTo: to}, nil
}
