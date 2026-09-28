package httpapi

import (
	"testing"

	"github.com/gamics-io/gamics/services/api/internal/competition"
)

func TestSlugifyProducesStorableSlugs(t *testing.T) {
	cases := map[string]string{
		"Nairobi Sunday Knockout": "nairobi-sunday-knockout",
		"  Coast   Rising  ":      "coast-rising",
		"Gamics Open #01":         "gamics-open-01",
		"eFootball — Mombasa":     "efootball-mombasa",
		"!!!":                     "",
		"":                        "",
	}
	for input, want := range cases {
		if got := slugify(input); got != want {
			t.Errorf("slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSlugValidationRejectsUnsafeShapes(t *testing.T) {
	valid := []string{"gamics", "nairobi-sunday-knockout", "open-01"}
	invalid := []string{"", "ab", "-leading", "trailing-", "double--hyphen", "Upper", "with space", "sym!bol"}
	for _, slug := range valid {
		if !validOrganizationSlug(slug) {
			t.Errorf("%q should be a valid slug", slug)
		}
	}
	for _, slug := range invalid {
		if validOrganizationSlug(slug) {
			t.Errorf("%q should be an invalid slug", slug)
		}
	}
	long := ""
	for range 49 {
		long += "a"
	}
	if validOrganizationSlug(long) {
		t.Error("a 49 character slug should be rejected")
	}
}

// The organizer console renders its lifecycle controls from allowedTransitions,
// so the list must match the domain state machine exactly.
func TestAllowedTransitionsMatchTheStateMachine(t *testing.T) {
	cases := map[competition.Status][]competition.Status{
		competition.StatusDraft:        {competition.StatusPublished, competition.StatusCancelled},
		competition.StatusPublished:    {competition.StatusRegistration, competition.StatusCancelled},
		competition.StatusRegistration: {competition.StatusCheckIn, competition.StatusCancelled},
		competition.StatusCheckIn:      {competition.StatusRunning, competition.StatusCancelled},
		competition.StatusRunning:      {competition.StatusCompleted, competition.StatusCancelled},
		competition.StatusCompleted:    {},
		competition.StatusCancelled:    {},
	}
	for from, want := range cases {
		got := competition.AllowedTransitions(from)
		if len(got) != len(want) {
			t.Fatalf("from %s: got %v, want %v", from, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("from %s: got %v, want %v", from, got, want)
			}
		}
		// Every advertised transition must actually be accepted by the model.
		for _, target := range got {
			model := competition.Competition{Status: from}
			if err := model.Transition(target); err != nil {
				t.Errorf("advertised %s -> %s but the model refused it: %v", from, target, err)
			}
		}
	}
}

func TestCompetitionAndEntryStatusValidation(t *testing.T) {
	for _, status := range []string{"draft", "published", "registration_open", "check_in", "running", "completed", "cancelled"} {
		if !validCompetitionStatus(status) {
			t.Errorf("%q should be a valid competition status", status)
		}
	}
	for _, status := range []string{"", "open", "REGISTRATION_OPEN", "deleted"} {
		if validCompetitionStatus(status) {
			t.Errorf("%q should not be a valid competition status", status)
		}
	}
	for _, status := range []string{"registered", "checked_in", "accepted", "withdrawal_pending", "withdrawn", "disqualified"} {
		if !validEntryStatus(status) {
			t.Errorf("%q should be a valid entry status", status)
		}
	}
	if validEntryStatus("pending") {
		t.Error("pending should not be a valid entry status")
	}
}
