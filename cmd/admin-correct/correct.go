package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

type options struct {
	table  string
	year   int
	date   string
	status tracker.Status
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var opts options
	var status string
	flags := flag.NewFlagSet("admin-correct", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.table, "table", "", "DynamoDB table name (required)")
	flags.IntVar(&opts.year, "year", 0, "record year (required)")
	flags.StringVar(&opts.date, "date", "", "record ISO date YYYY-MM-DD (required)")
	flags.StringVar(&status, "status", "", "FULL_DAY, HALF_DAY, PTO, or TIME_OFF (required)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments")
	}
	opts.status = tracker.Status(status)
	if err := opts.validate(); err != nil {
		return options{}, err
	}
	return opts, nil
}

func (o options) validate() error {
	if strings.TrimSpace(o.table) == "" {
		return fmt.Errorf("--table must not be blank")
	}
	day, err := time.Parse(time.DateOnly, o.date)
	if err != nil || day.Format(time.DateOnly) != o.date || o.year <= 0 || day.Year() != o.year {
		return fmt.Errorf("--year must be positive and match an exact YYYY-MM-DD --date")
	}
	if !tracker.IsUserStatus(o.status) {
		return fmt.Errorf("--status must be FULL_DAY, HALF_DAY, PTO, or TIME_OFF")
	}
	return nil
}

type responseStore interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
	UpdateUserResponseIfCurrent(context.Context, tracker.DayRecord, tracker.DayRecord) (bool, error)
}

// correctDay owns interactive orchestration only. Domain transitions and narrow
// persistence are shared with normal responses. Conflicts require fresh preview
// and confirmation; a matching current status intentionally performs no write.
func correctDay(ctx context.Context, db responseStore, opts options, input io.Reader, output io.Writer, now time.Time) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("correction timestamp must not be zero")
	}
	answers := bufio.NewScanner(input)
	for attempt := 1; attempt <= 3; attempt++ {
		current, exists, err := db.GetDay(ctx, opts.year, opts.date)
		if err != nil {
			return fmt.Errorf("read correction day %s: %w", opts.date, err)
		}
		if !exists {
			return fmt.Errorf("correction day %s not found", opts.date)
		}
		if current.Year != opts.year || current.Date != opts.date {
			return fmt.Errorf("correction day key does not match requested year/date")
		}
		updated, err := tracker.ApplyUserStatus(current, opts.status, now.UTC())
		if err != nil {
			return fmt.Errorf("apply correction %s: %w", opts.date, err)
		}
		if current.Status == opts.status {
			_, err := fmt.Fprintf(output, "%s is already %s; no effective state change is required. No write performed.\n", opts.date, opts.status)
			return err
		}
		if _, err := fmt.Fprintf(output, "Date: %s\nCurrent Status: %s\nRequested Status: %s\nCurrent HasBeenChanged: %t\nResulting HasBeenChanged: %t\nResulting ResponseSource: %s\nApply this correction? [y/N] ", opts.date, current.Status, opts.status, current.HasBeenChanged, updated.HasBeenChanged, updated.ResponseSource); err != nil {
			return fmt.Errorf("print correction preview: %w", err)
		}
		if !answers.Scan() {
			if err := answers.Err(); err != nil {
				return fmt.Errorf("read confirmation: %w", err)
			}
			_, err := fmt.Fprintln(output, "Correction cancelled; no write performed.")
			return err
		}
		answer := answers.Text()
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			_, err := fmt.Fprintln(output, "Correction cancelled; no write performed.")
			return err
		}
		saved, err := db.UpdateUserResponseIfCurrent(ctx, updated, current)
		if err != nil {
			return fmt.Errorf("save correction %s (attempt %d): %w", opts.date, attempt, err)
		}
		if saved {
			_, err := fmt.Fprintf(output, "Saved %s as %s.\n", opts.date, updated.Status)
			return err
		}
		if attempt < 3 {
			if _, err := fmt.Fprintln(output, "Record changed concurrently; rereading before another confirmation."); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("correction %s conflicted after 3 attempts; rerun the command", opts.date)
}
