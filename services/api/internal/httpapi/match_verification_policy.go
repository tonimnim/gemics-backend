package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

// Bounds of the per-competition matchVerification rules (R3, R6). The
// match_result_verifications CHECKs pin the same ranges on every snapshot.
const (
	defaultReportWindow       = 10 * time.Minute
	defaultReportReminderLead = 3 * time.Minute
	defaultResponseWindow     = 10 * time.Minute
	minimumVerificationWindow = 5 * time.Minute
	maximumVerificationWindow = 60 * time.Minute
	minimumReportReminderLead = time.Minute
)

var errInvalidMatchVerificationPolicy = errors.New("match verification policy is invalid")

// retiredMatchVerificationKeys are the single-submission settings the blind
// dual report replaced (D17).
var retiredMatchVerificationKeys = map[string]bool{
	"evidenceRequirement": true, "confirmationWindowMinutes": true, "autoConfirmEnabled": true, "refereeMonitoring": true,
}

const matchVerificationKeysHint = "use reportWindowMinutes, reminderBeforeDeadlineMinutes or responseWindowMinutes."

// matchVerificationRuleError is one rejected matchVerification rule. Its
// message names the offending key and is safe to return to the organizer.
type matchVerificationRuleError struct {
	message string
}

func (e *matchVerificationRuleError) Error() string {
	return errInvalidMatchVerificationPolicy.Error() + ": " + e.message
}

func (e *matchVerificationRuleError) Unwrap() error {
	return errInvalidMatchVerificationPolicy
}

func invalidMatchVerificationRule(message string) error {
	return &matchVerificationRuleError{message: message}
}

// matchVerificationSettings are the windows of the blind dual report. They are
// snapshotted into match_result_verifications at the first report, so a later
// rules edit never moves a live deadline.
type matchVerificationSettings struct {
	ReportWindow   time.Duration // default 10m, 5..60m
	ReminderLead   time.Duration // default 3m, 1m..ReportWindow-1m
	ResponseWindow time.Duration // default 10m, 5..60m
}

type matchVerificationOverrides struct {
	ReportWindowMinutes           *int `json:"reportWindowMinutes"`
	ReminderBeforeDeadlineMinutes *int `json:"reminderBeforeDeadlineMinutes"`
	ResponseWindowMinutes         *int `json:"responseWindowMinutes"`
}

func defaultMatchVerificationSettings() matchVerificationSettings {
	return matchVerificationSettings{
		ReportWindow:   defaultReportWindow,
		ReminderLead:   defaultReportReminderLead,
		ResponseWindow: defaultResponseWindow,
	}
}

// applyMatchVerificationOverrides is the tolerant read path. Stored rules may
// predate the current contract, so an out-of-range value keeps the current
// setting instead of failing the match room, and retired keys never reach
// here because plain json.Unmarshal ignores them.
func applyMatchVerificationOverrides(settings *matchVerificationSettings, override *matchVerificationOverrides) {
	if override == nil {
		return
	}
	if window, ok := verificationMinutes(override.ReportWindowMinutes, minimumVerificationWindow, maximumVerificationWindow); ok {
		settings.ReportWindow = window
	}
	if window, ok := verificationMinutes(override.ResponseWindowMinutes, minimumVerificationWindow, maximumVerificationWindow); ok {
		settings.ResponseWindow = window
	}
	if lead, ok := verificationMinutes(override.ReminderBeforeDeadlineMinutes, minimumReportReminderLead, maximumVerificationWindow); ok {
		settings.ReminderLead = lead
	}
	// The reminder must fire inside the window; the snapshot CHECK requires it.
	if settings.ReminderLead > settings.ReportWindow-time.Minute {
		settings.ReminderLead = settings.ReportWindow - time.Minute
	}
}

// validateMatchVerificationRules is the strict write path for organizer rules.
// Every key other than the three windows is rejected by name, including the
// retired single-submission settings, so an organizer learns they have no
// effect instead of having them silently ignored.
func validateMatchVerificationRules(raw []byte) error {
	var document struct {
		MatchVerification json.RawMessage `json:"matchVerification"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return invalidMatchVerificationRule("matchVerification must be an object.")
	}
	if len(document.MatchVerification) == 0 || bytes.Equal(document.MatchVerification, []byte("null")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document.MatchVerification, &fields); err != nil {
		return invalidMatchVerificationRule("matchVerification must be an object.")
	}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		switch {
		case key == "reportWindowMinutes", key == "reminderBeforeDeadlineMinutes", key == "responseWindowMinutes":
		case retiredMatchVerificationKeys[key]:
			return invalidMatchVerificationRule(key + " is no longer supported; " + matchVerificationKeysHint)
		default:
			return invalidMatchVerificationRule(key + " is not supported; " + matchVerificationKeysHint)
		}
	}
	var overrides matchVerificationOverrides
	if err := json.Unmarshal(document.MatchVerification, &overrides); err != nil {
		return invalidMatchVerificationRule("The match verification windows must be whole minutes.")
	}
	return overrides.validate()
}

// validate enforces the bounds after defaults are applied, so a lone reminder
// lead is checked against the default report window.
func (overrides matchVerificationOverrides) validate() error {
	settings := defaultMatchVerificationSettings()
	if overrides.ReportWindowMinutes != nil {
		window, ok := verificationMinutes(overrides.ReportWindowMinutes, minimumVerificationWindow, maximumVerificationWindow)
		if !ok {
			return invalidMatchVerificationRule("reportWindowMinutes must be between 5 and 60.")
		}
		settings.ReportWindow = window
	}
	if overrides.ResponseWindowMinutes != nil {
		if _, ok := verificationMinutes(overrides.ResponseWindowMinutes, minimumVerificationWindow, maximumVerificationWindow); !ok {
			return invalidMatchVerificationRule("responseWindowMinutes must be between 5 and 60.")
		}
	}
	if overrides.ReminderBeforeDeadlineMinutes != nil {
		lead, ok := verificationMinutes(overrides.ReminderBeforeDeadlineMinutes, minimumReportReminderLead, maximumVerificationWindow)
		if !ok {
			return invalidMatchVerificationRule("reminderBeforeDeadlineMinutes must be at least 1.")
		}
		settings.ReminderLead = lead
	}
	if settings.ReminderLead > settings.ReportWindow-time.Minute {
		return invalidMatchVerificationRule("reminderBeforeDeadlineMinutes must be less than the report window.")
	}
	return nil
}

// verificationMinutes converts an optional whole-minute rule into a duration
// when it lies within [minimum, maximum]. The bounds are compared in minutes,
// so a huge value cannot overflow into range.
func verificationMinutes(minutes *int, minimum, maximum time.Duration) (time.Duration, bool) {
	if minutes == nil || *minutes < int(minimum/time.Minute) || *minutes > int(maximum/time.Minute) {
		return 0, false
	}
	return time.Duration(*minutes) * time.Minute, true
}

// resolveMatchVerificationPolicy describes the windows a player is held to.
// Callers pass the snapshotted settings once a verification row exists.
func resolveMatchVerificationPolicy(settings matchVerificationSettings) matchVerificationPolicyResponse {
	response := matchVerificationPolicyResponse{
		ReportWindowSeconds:           int64(settings.ReportWindow / time.Second),
		ReminderBeforeDeadlineSeconds: int64(settings.ReminderLead / time.Second),
		ResponseWindowSeconds:         int64(settings.ResponseWindow / time.Second),
	}
	response.ScreenshotEvidence.MinItems = resultScreenshotMinItems
	response.ScreenshotEvidence.MaxItems = resultScreenshotMaxItems
	response.ScreenshotEvidence.MediaTypes = slices.Clone(screenshotMediaTypes)
	return response
}
