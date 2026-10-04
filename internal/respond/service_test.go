package respond

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var _ DayStore = (*store.Store)(nil)
var _ TokenVerifier = (*token.Signer)(nil)

type fakeStore struct {
	record            tracker.DayRecord
	exists            bool
	getErr, updateErr error
	reads, writes     int
	expected          []tracker.Status
	beforeUpdate      func(*fakeStore)
	conflict          bool
}

func (s *fakeStore) GetDay(_ context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	s.reads++
	if year != 2027 || date != "2027-01-01" {
		return tracker.DayRecord{}, false, errors.New("incorrect signed date lookup")
	}
	return s.record, s.exists, s.getErr
}
func (s *fakeStore) UpdateUserResponseIfCurrent(_ context.Context, r tracker.DayRecord, expected tracker.DayRecord) (bool, error) {
	s.writes++
	s.expected = append(s.expected, expected.Status)
	if s.updateErr != nil {
		return false, s.updateErr
	}
	if s.beforeUpdate != nil {
		s.beforeUpdate(s)
	}
	if s.conflict || !s.exists || s.record.Status != expected.Status || (tracker.IsUserStatus(expected.Status) && !s.record.RespondedAt.Equal(expected.RespondedAt)) {
		return false, nil
	}
	// Mirror the narrow write: preserve unrelated fields from current persistence.
	s.record.Status, s.record.WorkFraction, s.record.PTOFraction = r.Status, r.WorkFraction, r.PTOFraction
	s.record.RespondedAt, s.record.ResponseSource, s.record.HasBeenChanged = r.RespondedAt, r.ResponseSource, r.HasBeenChanged
	return true, nil
}

type countingVerifier struct {
	signer *token.Signer
	calls  int
}

func (v *countingVerifier) Verify(raw string) (token.Claims, error) {
	v.calls++
	return v.signer.Verify(raw)
}
func setup(t *testing.T, status tracker.Status) (*Service, *fakeStore, *countingVerifier, time.Time) {
	t.Helper()
	at := time.Date(2027, 1, 1, 12, 0, 0, 123456789, time.FixedZone("app", -7*3600))
	record := tracker.NewPendingDay(at)
	var err error
	if status == tracker.StatusNoResponse {
		record, err = tracker.FinalizePending(record, at.Add(-time.Hour))
	} else if tracker.IsUserStatus(status) {
		record, err = tracker.ApplyUserStatus(record, status, at.Add(-time.Hour))
	}
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeStore{record: record, exists: true}
	signer, err := token.New([]byte("test secret"))
	if err != nil {
		t.Fatal(err)
	}
	verifier := &countingVerifier{signer: signer}
	service, err := New(s, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return service, s, verifier, at
}
func signed(t *testing.T, v *countingVerifier, status tracker.Status) string {
	t.Helper()
	raw, err := v.signer.Sign(token.Claims{Date: "2027-01-01", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPreview(t *testing.T) {
	for _, tc := range []struct {
		current, requested           tracker.Status
		currentLabel, requestedLabel string
		correction                   bool
	}{
		{tracker.StatusPending, tracker.StatusFullDay, "Pending", "Full Day", false},
		{tracker.StatusFullDay, tracker.StatusPTO, "Full Day", "PTO", true},
		{tracker.StatusHalfDay, tracker.StatusTimeOff, "Half Day", "Time Off", true},
		{tracker.StatusPTO, tracker.StatusHalfDay, "PTO", "Half Day", true},
		{tracker.StatusTimeOff, tracker.StatusFullDay, "Time Off", "Full Day", true},
		{tracker.StatusFullDay, tracker.StatusFullDay, "Full Day", "Full Day", false},
		{tracker.StatusNoResponse, tracker.StatusPTO, "No Response", "PTO", false},
	} {
		t.Run(string(tc.current)+"/"+string(tc.requested), func(t *testing.T) {
			service, s, v, _ := setup(t, tc.current)
			original := s.record
			raw := signed(t, v, tc.requested)
			got, err := service.Preview(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			want := Confirmation{Token: raw, Date: "2027-01-01", FriendlyDate: "January 1, 2027", RequestedStatus: tc.requested, RequestedLabel: tc.requestedLabel, CurrentStatus: tc.current, CurrentLabel: tc.currentLabel, IsCorrection: tc.correction}
			if got != want || s.writes != 0 || !reflect.DeepEqual(s.record, original) {
				t.Fatalf("incorrect or mutating preview: %+v", got)
			}
		})
	}
}

func TestSubmitTransitions(t *testing.T) {
	for _, tc := range []struct {
		from, to  tracker.Status
		work, pto float64
		changed   bool
	}{
		{tracker.StatusPending, tracker.StatusFullDay, 1, 0, false},
		{tracker.StatusPending, tracker.StatusHalfDay, .5, 0, false},
		{tracker.StatusPending, tracker.StatusPTO, 0, 1, false},
		{tracker.StatusPending, tracker.StatusTimeOff, 0, 0, false},
		{tracker.StatusNoResponse, tracker.StatusPTO, 0, 1, false},
		{tracker.StatusFullDay, tracker.StatusPTO, 0, 1, true},
		{tracker.StatusPTO, tracker.StatusHalfDay, .5, 0, true},
		{tracker.StatusTimeOff, tracker.StatusFullDay, 1, 0, true},
	} {
		t.Run(string(tc.from)+"/"+string(tc.to), func(t *testing.T) {
			service, s, v, now := setup(t, tc.from)
			original := s.record
			got, err := service.Submit(context.Background(), signed(t, v, tc.to), now)
			if err != nil {
				t.Fatal(err)
			}
			source := tracker.ResponseSourceUser
			if tc.from == tracker.StatusNoResponse {
				source = tracker.ResponseSourceLateUser
			}
			if got.Status != tc.to || got.WorkFraction == nil || *got.WorkFraction != tc.work || got.PTOFraction != tc.pto || got.HasBeenChanged != tc.changed || got.ResponseSource != source || got.RespondedAt != now.UTC() || got.FinalizedAt != original.FinalizedAt {
				t.Fatalf("incorrect response %+v", got)
			}
			if !reflect.DeepEqual(got, s.record) || s.writes != 1 || s.expected[0] != tc.from || v.calls != 1 {
				t.Fatal("incorrect conditional persistence")
			}
		})
	}
}

func TestRepeat(t *testing.T) {
	for _, changed := range []bool{false, true} {
		service, s, v, now := setup(t, tracker.StatusFullDay)
		s.record.HasBeenChanged = changed
		original := s.record
		got, err := service.Submit(context.Background(), signed(t, v, tracker.StatusFullDay), now)
		if err != nil || !reflect.DeepEqual(got, original) || !reflect.DeepEqual(s.record, original) || s.writes != 1 || s.expected[0] != tracker.StatusFullDay {
			t.Fatalf("repeat changed metadata or skipped condition: %+v, %v", got, err)
		}
	}
}

func TestResponseRaces(t *testing.T) {
	for _, mode := range []string{"finalizer", "email timestamp", "correction", "repeat correction", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			from, requested := tracker.StatusPending, tracker.StatusPTO
			if mode == "correction" {
				from = tracker.StatusFullDay
				requested = tracker.StatusTimeOff
			}
			if mode == "repeat correction" {
				from = tracker.StatusFullDay
				requested = tracker.StatusFullDay
			}
			service, s, v, now := setup(t, from)
			if mode == "exhausted" {
				s.conflict = true
			} else {
				s.beforeUpdate = func(s *fakeStore) {
					if s.writes != 1 {
						return
					}
					var err error
					switch mode {
					case "finalizer":
						s.record, err = tracker.FinalizePending(s.record, now.Add(-time.Minute))
					case "email timestamp":
						s.record.EmailSentAt = now.UTC()
					default:
						s.record, err = tracker.ApplyUserStatus(s.record, tracker.StatusPTO, now.Add(-time.Minute))
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := service.Submit(context.Background(), signed(t, v, requested), now)
			if mode == "exhausted" {
				if !errors.Is(err, ErrConflict) || s.reads != 3 || s.writes != 3 || v.calls != 1 {
					t.Fatalf("incorrect bounded retry: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != requested || s.record.Status != requested || v.calls != 1 {
				t.Fatal("response failed")
			}
			if mode == "email timestamp" {
				if s.record.EmailSentAt != now.UTC() || s.writes != 1 {
					t.Fatal("concurrent email timestamp lost")
				}
			} else {
				if s.reads != 2 || s.writes != 2 {
					t.Fatal("conflict did not reread/reapply")
				}
				if mode == "finalizer" {
					if got.ResponseSource != tracker.ResponseSourceLateUser || got.FinalizedAt.IsZero() || got.HasBeenChanged {
						t.Fatal("finalizer race was not a late initial response")
					}
				} else if !got.HasBeenChanged || got.RespondedAt != now.UTC() {
					t.Fatal("correction metadata incorrect")
				}
			}
		})
	}
}

func TestServiceErrors(t *testing.T) {
	failure := errors.New("storage failure")
	for _, preview := range []bool{true, false} {
		for _, mode := range []string{"invalid token", "missing", "get", "update", "zero time"} {
			if preview && (mode == "update" || mode == "zero time") {
				continue
			}
			t.Run(mode+map[bool]string{true: "/preview", false: "/submit"}[preview], func(t *testing.T) {
				service, s, v, now := setup(t, tracker.StatusPending)
				raw := signed(t, v, tracker.StatusPTO)
				var want error
				switch mode {
				case "invalid token":
					raw = "invalid"
					want = ErrInvalidToken
				case "missing":
					s.exists = false
					want = ErrDayNotFound
				case "get":
					s.getErr = failure
					want = failure
				case "update":
					s.updateErr = failure
					want = failure
				case "zero time":
					now = time.Time{}
				}
				var err error
				if preview {
					_, err = service.Preview(context.Background(), raw)
				} else {
					_, err = service.Submit(context.Background(), raw, now)
				}
				if err == nil || (want != nil && !errors.Is(err, want)) {
					t.Fatalf("wrong error: %v", err)
				}
				if mode == "get" || mode == "update" {
					if !strings.Contains(err.Error(), "2027-01-01") {
						t.Fatal("missing date context")
					}
					if errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrDayNotFound) || errors.Is(err, ErrConflict) {
						t.Fatal("storage failure misclassified")
					}
				}
				if preview && s.writes != 0 {
					t.Fatal("preview wrote")
				}
				if (mode == "invalid token" || mode == "zero time") && (s.reads != 0 || s.writes != 0) {
					t.Fatal("invalid input accessed storage")
				}
				if mode == "invalid token" && err.Error() != ErrInvalidToken.Error() {
					t.Fatal("verification details leaked")
				}
			})
		}
	}
	service, s, v, now := setup(t, tracker.StatusPending)
	s.record.Status = "invalid"
	if _, err := service.Submit(context.Background(), signed(t, v, tracker.StatusPTO), now); err == nil || s.writes != 0 {
		t.Fatal("invalid domain transition persisted")
	}
	if service, err := New(nil, v); service != nil || err == nil {
		t.Fatal("nil store accepted")
	}
	if service, err := New(s, nil); service != nil || err == nil {
		t.Fatal("nil verifier accepted")
	}
}

func TestRepeatedResponseABA(t *testing.T) {
	service, s, v, now := setup(t, tracker.StatusFullDay)
	original := s.record
	t2, t3 := now.Add(-30*time.Minute).UTC(), now.Add(-15*time.Minute).UTC()
	s.beforeUpdate = func(s *fakeStore) {
		if s.writes != 1 {
			return
		}
		var err error
		s.record, err = tracker.ApplyUserStatus(s.record, tracker.StatusPTO, t2)
		if err != nil {
			t.Fatal(err)
		}
		s.record, err = tracker.ApplyUserStatus(s.record, tracker.StatusFullDay, t3)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := service.Submit(context.Background(), signed(t, v, tracker.StatusFullDay), now)
	if err != nil {
		t.Fatal(err)
	}
	if s.reads != 2 || s.writes != 2 || v.calls != 1 {
		t.Fatalf("expected two read/write attempts and one verification: reads=%d writes=%d verifies=%d", s.reads, s.writes, v.calls)
	}
	if got.Status != tracker.StatusFullDay || got.RespondedAt != t3 || !got.HasBeenChanged || got.RespondedAt.Equal(original.RespondedAt) || !reflect.DeepEqual(got, s.record) {
		t.Fatalf("stale repeated response restored metadata: %+v", got)
	}
}
