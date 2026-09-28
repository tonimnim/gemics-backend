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

// AllowedTransitions lists, in lifecycle order, the statuses reachable from the
// given one. Organizer clients render controls from this instead of hardcoding
// the state machine, so the server stays the single source of truth for what an
// organizer may do next.
func AllowedTransitions(from Status) []Status {
	allowed, ok := transitions[from]
	if !ok {
		return []Status{}
	}
	ordered := []Status{StatusPublished, StatusRegistration, StatusCheckIn, StatusRunning, StatusCompleted, StatusCancelled}
	result := make([]Status, 0, len(allowed))
	for _, candidate := range ordered {
		if _, ok := allowed[candidate]; ok {
			result = append(result, candidate)
		}
	}
	return result
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
