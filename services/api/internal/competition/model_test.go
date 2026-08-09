package competition

import (
	"testing"
	"time"
)

func TestCompetitionTransition(t *testing.T) {
	c := Competition{Status: StatusDraft}
	for _, next := range []Status{StatusPublished, StatusRegistration, StatusCheckIn, StatusRunning, StatusCompleted} {
		if err := c.Transition(next); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	if err := c.Transition(StatusRunning); err == nil {
		t.Fatal("expected completed competition to reject transition")
	}
}

func TestCompetitionSchedule(t *testing.T) {
	now := time.Now()
	c := Competition{
		Name: "Nairobi Open", OrganizationID: "org-1", GameID: "efootball-mobile",
		MaxEntries: 32, RegistrationOpen: now, RegistrationEnd: now.Add(time.Hour), StartsAt: now.Add(2 * time.Hour),
	}
	if err := c.ValidateSchedule(); err != nil {
		t.Fatalf("expected valid schedule: %v", err)
	}
}
