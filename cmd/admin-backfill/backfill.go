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
	flags := flag.NewFlagSet("admin-backfill", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&opts.table, "table", "", "DynamoDB table name (required)")
	flags.IntVar(&opts.year, "year", 0, "historical record year (required)")
	flags.StringVar(&opts.date, "date", "", "historical ISO date YYYY-MM-DD (required)")
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

type backfillStore interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
	CreateDay(context.Context, tracker.DayRecord) (bool, error)
}

// backfillDay creates only an absent day. No correction or retry can replace a
// record that exists at the initial read or wins the conditional-create race.
func backfillDay(ctx context.Context, db backfillStore, opts options, input io.Reader, output io.Writer, now time.Time) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("administrative backfill timestamp must not be zero")
	}
	_, exists, err := db.GetDay(ctx, opts.year, opts.date)
	if err != nil {
		return fmt.Errorf("read backfill day %s: %w", opts.date, err)
	}
	if exists {
		return fmt.Errorf("day %s already exists; no write performed; use admin-correct for a status correction", opts.date)
	}
	day, _ := time.Parse(time.DateOnly, opts.date) // Validated exact calendar date, independent of timezone.
	record, err := tracker.NewAdminBackfillDay(day, opts.status, now)
	if err != nil {
		return fmt.Errorf("prepare backfill %s: %w", opts.date, err)
	}
	if _, err := fmt.Fprintf(output, "Date: %s\nStatus: %s\nWork Fraction: %g\nPTO Fraction: %g\nWeekend: %t\nResponse Source: %s\nResponded At: %s\nCreate this historical record? [y/N] ", record.Date, record.Status, *record.WorkFraction, record.PTOFraction, record.IsWeekend, record.ResponseSource, record.RespondedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("print backfill preview: %w", err)
	}
	answers := bufio.NewScanner(input)
	if !answers.Scan() {
		if err := answers.Err(); err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		_, err := fmt.Fprintln(output, "Backfill cancelled; no write performed.")
		return err
	}
	answer := answers.Text()
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		_, err := fmt.Fprintln(output, "Backfill cancelled; no write performed.")
		return err
	}
	created, err := db.CreateDay(ctx, record)
	if err != nil {
		return fmt.Errorf("create backfill day %s: %w", opts.date, err)
	}
	if !created {
		return fmt.Errorf("day %s now exists; not created; use admin-correct for a status correction", opts.date)
	}
	_, err = fmt.Fprintf(output, "Created %s as %s. The DynamoDB Stream will project it to Sheets.\n", record.Date, record.Status)
	return err
}
