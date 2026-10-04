// Package respond implements read-only confirmation and signed response submission.
package respond

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var (
	ErrInvalidToken = errors.New("invalid response token")
	ErrDayNotFound  = errors.New("day record not found")
	ErrConflict     = errors.New("response update conflict")
)

// TokenVerifier authenticates and validates signed date/status claims.
type TokenVerifier interface {
	Verify(string) (token.Claims, error)
}

// DayStore requires consistent reads and narrow conditional response writes.
type DayStore interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
	UpdateUserResponseIfCurrent(context.Context, tracker.DayRecord, tracker.DayRecord) (bool, error)
}

type Service struct {
	store    DayStore
	verifier TokenVerifier
}

func New(store DayStore, verifier TokenVerifier) (*Service, error) {
	if store == nil {
		return nil, errors.New("response service requires a store")
	}
	if verifier == nil {
		return nil, errors.New("response service requires a token verifier")
	}
	return &Service{store: store, verifier: verifier}, nil
}

type Confirmation struct {
	Token           string
	Date            string
	FriendlyDate    string
	RequestedStatus tracker.Status
	RequestedLabel  string
	CurrentStatus   tracker.Status
	CurrentLabel    string
	IsCorrection    bool
}

func label(status tracker.Status) string {
	switch status {
	case tracker.StatusFullDay:
		return "Full Day"
	case tracker.StatusHalfDay:
		return "Half Day"
	case tracker.StatusPTO:
		return "PTO"
	case tracker.StatusTimeOff:
		return "Time Off"
	case tracker.StatusPending:
		return "Pending"
	case tracker.StatusNoResponse:
		return "No Response"
	default:
		return "Unknown"
	}
}

func (s *Service) verify(raw string) (token.Claims, time.Time, error) {
	claims, err := s.verifier.Verify(raw)
	if err != nil {
		return token.Claims{}, time.Time{}, ErrInvalidToken
	}
	day, err := time.Parse("2006-01-02", claims.Date)
	if err != nil || day.Format("2006-01-02") != claims.Date || !tracker.IsUserStatus(claims.Status) {
		return token.Claims{}, time.Time{}, ErrInvalidToken
	}
	return claims, day, nil
}

func (s *Service) getDay(ctx context.Context, year int, date string) (tracker.DayRecord, error) {
	record, exists, err := s.store.GetDay(ctx, year, date)
	if err != nil {
		return tracker.DayRecord{}, fmt.Errorf("get response day %s: %w", date, err)
	}
	if !exists {
		return tracker.DayRecord{}, fmt.Errorf("response day %s: %w", date, ErrDayNotFound)
	}
	return record, nil
}

// Preview returns confirmation data without performing any write.
func (s *Service) Preview(ctx context.Context, rawToken string) (Confirmation, error) {
	claims, day, err := s.verify(rawToken)
	if err != nil {
		return Confirmation{}, err
	}
	current, err := s.getDay(ctx, day.Year(), claims.Date)
	if err != nil {
		return Confirmation{}, err
	}
	return Confirmation{
		Token: rawToken, Date: claims.Date, FriendlyDate: day.Format("January 2, 2006"),
		RequestedStatus: claims.Status, RequestedLabel: label(claims.Status),
		CurrentStatus: current.Status, CurrentLabel: label(current.Status),
		IsCorrection: tracker.IsUserStatus(current.Status) && current.Status != claims.Status,
	}, nil
}

// Submit verifies once and retries up to three conditional conflicts. The returned
// record is the applied read snapshot; unrelated concurrent fields may be newer
// in persistence. Repeated selections retain the original response timestamp.
func (s *Service) Submit(ctx context.Context, rawToken string, now time.Time) (tracker.DayRecord, error) {
	if now.IsZero() {
		return tracker.DayRecord{}, errors.New("submit response: timestamp must not be zero")
	}
	claims, day, err := s.verify(rawToken)
	if err != nil {
		return tracker.DayRecord{}, err
	}
	for attempt := 1; attempt <= 3; attempt++ {
		current, err := s.getDay(ctx, day.Year(), claims.Date)
		if err != nil {
			return tracker.DayRecord{}, err
		}
		updated, err := tracker.ApplyUserStatus(current, claims.Status, now.UTC())
		if err != nil {
			return tracker.DayRecord{}, fmt.Errorf("apply response %s: %w", claims.Date, err)
		}
		saved, err := s.store.UpdateUserResponseIfCurrent(ctx, updated, current)
		if err != nil {
			return tracker.DayRecord{}, fmt.Errorf("save response %s (attempt %d): %w", claims.Date, attempt, err)
		}
		if saved {
			return updated, nil
		}
	}
	return tracker.DayRecord{}, fmt.Errorf("submit response %s after 3 attempts: %w", claims.Date, ErrConflict)
}
