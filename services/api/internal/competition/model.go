package competition

import (
	"errors"
	"fmt"
	"time"
)

type Status string

const (
	StatusDraft        Status = "draft"
	StatusPublished    Status = "published"
	StatusRegistration Status = "registration_open"
	StatusCheckIn      Status = "check_in"
	StatusRunning      Status = "running"
	StatusCompleted    Status = "completed"
	StatusCancelled    Status = "cancelled"
)

type Format string

const (
	SingleElimination Format = "single_elimination"
	DoubleElimination Format = "double_elimination"
	RoundRobin        Format = "round_robin"
)

type Competition struct {
	ID               string
	OrganizationID   string
	GameID           string
	Name             string
	Slug             string
	Status           Status
	Format           Format
	RegistrationOpen time.Time
	RegistrationEnd  time.Time
	StartsAt         time.Time
	MaxEntries       int
	RulesVersion     int
}

var transitions = map[Status]map[Status]struct{}{
	StatusDraft:        {StatusPublished: {}, StatusCancelled: {}},
	StatusPublished:    {StatusRegistration: {}, StatusCancelled: {}},
	StatusRegistration: {StatusCheckIn: {}, StatusCancelled: {}},
	StatusCheckIn:      {StatusRunning: {}, StatusCancelled: {}},
	StatusRunning:      {StatusCompleted: {}, StatusCancelled: {}},
}

func (c *Competition) Transition(to Status) error {
	allowed, ok := transitions[c.Status]
	if !ok {
		return fmt.Errorf("competition in terminal state %q", c.Status)
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("invalid competition transition %q -> %q", c.Status, to)
	}
	c.Status = to
	return nil
}

func (c Competition) ValidateSchedule() error {
	if c.Name == "" || c.OrganizationID == "" || c.GameID == "" {
		return errors.New("name, organization and game are required")
	}
	if c.MaxEntries < 2 {
		return errors.New("a competition requires at least two entries")
	}
	if !c.RegistrationOpen.Before(c.RegistrationEnd) {
		return errors.New("registration must end after it opens")
	}
	if c.StartsAt.Before(c.RegistrationEnd) {
		return errors.New("competition cannot start before registration ends")
	}
	return nil
}

type ResultStatus string

const (
	ResultPending   ResultStatus = "pending_confirmation"
	ResultConfirmed ResultStatus = "confirmed"
	ResultDisputed  ResultStatus = "disputed"
	ResultResolved  ResultStatus = "resolved"
)

// ResultSubmission is append-only. Corrections create a new submission and an
// audit event instead of silently changing competitive history.
type ResultSubmission struct {
	ID           string
	MatchID      string
	SubmittedBy  string
	HomeScore    int
	AwayScore    int
	EvidenceKeys []string
	Status       ResultStatus
	SubmittedAt  time.Time
	ConfirmedBy  string
	ConfirmedAt  *time.Time
	SupersedesID string
}
