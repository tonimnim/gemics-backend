package httpapi

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The OpenAPI files are the client contract for blind result verification.
// These source-shape tests pin what generated clients depend on: the version,
// the retired referee surface, the new operations and enums, the blindness
// wording and the error codes the handlers can actually return.

// openAPIFile reads a contract file. readSourceFile normalizes line endings,
// so the block parser below behaves the same on a CRLF checkout.
func openAPIFile(t *testing.T, name string) string {
	t.Helper()
	return readSourceFile(t, filepath.Join("..", "..", "openapi", name))
}

// openAPIBlock returns the block that starts with the line indent+header+":"
// and runs until the next line indented no deeper than the header.
func openAPIBlock(t *testing.T, contract, header string, indent int) string {
	t.Helper()
	lines := strings.Split(contract, "\n")
	marker := strings.Repeat(" ", indent) + header + ":"
	start := slices.Index(lines, marker)
	if start < 0 {
		t.Fatalf("contract has no %q block", marker)
	}
	end := start + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" ||
		len(lines[end])-len(strings.TrimLeft(lines[end], " ")) > indent) {
		end++
	}
	return strings.Join(lines[start:end], "\n")
}

// openAPIEnum reads the first enum of a block, in flow or block style.
func openAPIEnum(t *testing.T, block string) []string {
	t.Helper()
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "enum:") {
			continue
		}
		if flow := strings.TrimSpace(strings.TrimPrefix(trimmed, "enum:")); flow != "" {
			values := strings.Split(strings.Trim(flow, "[]"), ",")
			for position := range values {
				values[position] = strings.Trim(strings.TrimSpace(values[position]), `"'`)
			}
			return values
		}
		var values []string
		for _, item := range lines[index+1:] {
			value, ok := strings.CutPrefix(strings.TrimSpace(item), "- ")
			if !ok {
				break
			}
			values = append(values, strings.Trim(value, `"'`))
		}
		return values
	}
	t.Fatalf("block has no enum:\n%s", block)
	return nil
}

// openAPIWords collapses folded YAML text so wording can be matched across
// line breaks.
func openAPIWords(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func TestOpenAPIContractVersionAndRetiredSurface(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	if !strings.Contains(contract, "\n  version: 0.7.0\n") {
		t.Error("openapi.yaml info.version is not 0.7.0")
	}
	componentsAt := strings.Index(contract, "\ncomponents:\n")
	if componentsAt < 0 {
		t.Fatal("openapi.yaml has no components section")
	}
	paths, components := contract[:componentsAt], contract[componentsAt:]
	for _, retired := range []string{"referee-case", "result-submissions", "dispute"} {
		if strings.Contains(paths, retired) {
			t.Errorf("openapi.yaml paths still mention %q", retired)
		}
	}
	for _, retired := range []string{"ResultSubmission", "ResultDecision", "PendingMatchResult", "Referee", "dispute.resolve"} {
		if strings.Contains(components, retired) {
			t.Errorf("openapi.yaml components still contain %q", retired)
		}
	}
	parameters := openAPIBlock(t, contract, "parameters", 2)
	if !strings.Contains(parameters, "\n    IdempotencyKey:\n") {
		t.Error("components.parameters has no IdempotencyKey")
	}
	for _, name := range []string{"referee-operations.paths.yaml", "participant-case-media.paths.yaml"} {
		if _, err := os.Stat(filepath.Join("..", "..", "openapi", name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("retired fragment %s still exists (err=%v)", name, err)
		}
	}
}

func TestOpenAPIResultVerificationOperations(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	operations := []struct {
		fragment, path, operationID string
		idempotent                  bool
	}{
		{"match-score-reports.paths.yaml", "/v1/matches/{matchId}/score-reports", "createScoreReport", true},
		{"match-score-reports.paths.yaml", "/v1/matches/{matchId}/score-reports/final", "createFinalScoreReport", true},
		{"result-reviews.paths.yaml", "/v1/admin/result-reviews", "listResultReviews", false},
		{"result-reviews.paths.yaml", "/v1/admin/result-reviews/{id}", "getResultReview", false},
		{"result-reviews.paths.yaml", "/v1/admin/result-reviews/{id}/decisions", "decideResultReview", true},
		{"result-reviews.paths.yaml", "/v1/admin/player-strikes", "listPlayerStrikes", false},
		{"result-reviews.paths.yaml", "/v1/admin/player-strikes/{id}/revocations", "revokePlayerStrike", true},
	}
	for _, operation := range operations {
		for name, source := range map[string]string{"openapi.yaml": contract, operation.fragment: openAPIFile(t, operation.fragment)} {
			block := openAPIBlock(t, source, operation.path, 2)
			if !strings.Contains(block, "operationId: "+operation.operationID+"\n") {
				t.Errorf("%s %s does not declare operationId %s", name, operation.path, operation.operationID)
			}
			if operation.idempotent && !strings.Contains(block, "#/components/parameters/IdempotencyKey") {
				t.Errorf("%s %s does not require the IdempotencyKey header", name, operation.path)
			}
		}
	}
}

func TestOpenAPIResultVerificationEnums(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	enums := []struct {
		fragment, schema string
		want             []string
	}{
		{"match-score-reports.paths.yaml", "MatchLifecycle", []string{"assigned", "ready_for_check_in", "checked_in",
			"report_required", "awaiting_opponent_report", "mismatch_response_required", "awaiting_opponent_response",
			"awaiting_resolution", "under_review", "forfeited", "completed", "cancelled", "out_of_competition"}},
		{"match-score-reports.paths.yaml", "MatchAllowedAction", []string{"check_in", "report_score", "submit_final_score"}},
		{"match-score-reports.paths.yaml", "MatchCompletionReason", []string{"played", "walkover", "double_no_show",
			"timeout_forfeit", "reset_not_required", "correction_voided", "report_timeout", "response_timeout",
			"no_result_reported", "platform_review", "competition_cancelled", "referee", "null"}},
		{"result-reviews.paths.yaml", "ResultReviewDecisionCode", []string{"accept_home", "accept_away", "corrected_score", "remove_both"}},
		{"result-reviews.paths.yaml", "ResultReviewStatus", []string{"queued", "decided", "closed"}},
		{"result-reviews.paths.yaml", "ResultReviewReason", []string{"reports_differ", "evidence_unavailable"}},
	}
	for _, enum := range enums {
		for name, source := range map[string]string{"openapi.yaml": contract, enum.fragment: openAPIFile(t, enum.fragment)} {
			got := openAPIEnum(t, openAPIBlock(t, source, enum.schema, 4))
			if !slices.Equal(got, enum.want) {
				t.Errorf("%s %s enum = %v, want %v", name, enum.schema, got, enum.want)
			}
		}
	}
}

// TestOpenAPIPlayerSchemasAreBlind pins the blindness wording on every
// player-facing schema that exists before a match is resolved (R2).
func TestOpenAPIPlayerSchemasAreBlind(t *testing.T) {
	const wording = "never contains the opponent's score before the match is resolved"
	contract := openAPIFile(t, "openapi.yaml")
	fragment := openAPIFile(t, "match-score-reports.paths.yaml")
	for name, schemas := range map[string]struct {
		source string
		names  []string
	}{
		"openapi.yaml": {contract, []string{"MatchRoom", "ResultVerification", "ScoreReportView",
			"ScoreReportResponseEnvelope"}},
		"match-score-reports.paths.yaml": {fragment, []string{"ResultVerification", "ScoreReportView",
			"ScoreReportResponseEnvelope"}},
	} {
		for _, schema := range schemas.names {
			if !strings.Contains(strings.ToLower(openAPIWords(openAPIBlock(t, schemas.source, schema, 4))), wording) {
				t.Errorf("%s %s does not state that it %s", name, schema, wording)
			}
		}
	}
	if !strings.Contains(openAPIWords(fragment), "Responses, errors and pushes never contain the opponent's score") {
		t.Error("match-score-reports.paths.yaml header does not state the blindness rule")
	}
}

var openAPIErrorCodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`Code:\s*"([a-z_]+)"`),
	regexp.MustCompile(`writeError\(w,\s*http\.Status\w+,\s*"([a-z_]+)"`),
}

// openAPIHandlerErrorCodes collects every literal error code the given source
// files can write.
func openAPIHandlerErrorCodes(t *testing.T, files ...string) []string {
	t.Helper()
	var codes []string
	for _, file := range files {
		source := readSourceFile(t, file)
		for _, pattern := range openAPIErrorCodePatterns {
			for _, match := range pattern.FindAllStringSubmatch(source, -1) {
				codes = append(codes, match[1])
			}
		}
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

// TestOpenAPIListsEveryResultErrorCode derives the error codes from the
// handler sources, so a new code cannot ship undocumented.
func TestOpenAPIListsEveryResultErrorCode(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	shared := []string{"invalid_request", "idempotency_key_required", "invalid_idempotency_key"}
	surfaces := []struct {
		fragment string
		paths    []string
		sources  []string
		extra    []string
	}{
		{
			fragment: "match-score-reports.paths.yaml",
			paths:    []string{"/v1/matches/{matchId}/score-reports", "/v1/matches/{matchId}/score-reports/final"},
			sources:  []string{"match_result_reports.go", "match_result_verification.go"},
			extra:    shared,
		},
		{
			fragment: "result-reviews.paths.yaml",
			paths: []string{"/v1/admin/result-reviews", "/v1/admin/result-reviews/{id}",
				"/v1/admin/result-reviews/{id}/decisions", "/v1/admin/player-strikes",
				"/v1/admin/player-strikes/{id}/revocations"},
			sources: []string{"result_review_handlers.go", "result_review_decision.go", "player_strike_handlers.go"},
			extra:   append([]string{"platform_access_denied", "invalid_limit", "invalid_cursor"}, shared...),
		},
	}
	for _, surface := range surfaces {
		handlerCodes := openAPIHandlerErrorCodes(t, surface.sources...)
		if len(handlerCodes) == 0 {
			t.Fatalf("no error codes found in %v; the extraction patterns no longer match the handlers", surface.sources)
		}
		codes := append(handlerCodes, surface.extra...)
		for name, source := range map[string]string{"openapi.yaml": contract, surface.fragment: openAPIFile(t, surface.fragment)} {
			var documented strings.Builder
			for _, path := range surface.paths {
				documented.WriteString(openAPIBlock(t, source, path, 2))
			}
			for _, code := range codes {
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(code) + `\b`).MatchString(documented.String()) {
					t.Errorf("%s does not document error code %s on %v", name, code, surface.paths)
				}
			}
		}
	}
}

var openAPIFlowKey = regexp.MustCompile(`[\w$"'-]+: `)

// openAPIStrayFlowKeys returns the text a YAML parser would read as a stray
// key with a null value. Inside a flow mapping a plain scalar ends at the first
// comma, so `{ description: a, b }` silently adds the key "b".
func openAPIStrayFlowKeys(line string) []string {
	var stray []string
	for _, match := range openAPIFlowKey.FindAllStringIndex(line, -1) {
		start := match[1]
		if !openAPIInFlowMapping(line[:match[0]]) || start >= len(line) || strings.ContainsRune(`"'[{`, rune(line[start])) {
			continue
		}
		end := start + strings.IndexAny(line[start:]+"}", ",{}[]")
		if end >= len(line) || line[end] != ',' {
			continue
		}
		next := line[end+1:]
		next = strings.TrimSpace(next[:strings.IndexAny(next+"}", ",{}[]")])
		if next != "" && !strings.Contains(next, ":") {
			stray = append(stray, next)
		}
	}
	return stray
}

// openAPIInFlowMapping reports whether prefix ends inside a flow mapping and
// outside any quoted scalar. A quote opens a scalar only where a value starts,
// so an apostrophe inside plain text is ignored.
func openAPIInFlowMapping(prefix string) bool {
	depth := 0
	var quote rune
	valueStart := true
	for _, char := range prefix {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case (char == '"' || char == '\'') && valueStart:
			quote = char
		case char == '{':
			depth++
		case char == '}':
			depth--
		}
		if char != ' ' {
			valueStart = strings.ContainsRune(":,{[", char)
		}
	}
	return depth > 0 && quote == 0
}

func TestOpenAPIStrayFlowKeysDetection(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{`kind: { type: string, description: legacy uploads, which are failed. }`, []string{"which are failed."}},
		{`"404": { description: Player is absent, private, suspended or deleted. }`, []string{"private"}},
		{`- { name: id, in: path, required: true, schema: { type: string, format: uuid } }`, nil},
		{`kind: { type: string, enum: [image, video], description: "legacy uploads, which are failed." }`, nil},
		{`description: invalid_user, invalid_status, invalid_limit.`, nil},
		{`note: { type: string, description: 'a, b' }`, nil},
		{`note: { type: string, description: "a, b: c, d" }`, nil},
		{`note: { description: the admin's own, or { a } }`, []string{"or"}},
	}
	for _, test := range tests {
		if got := openAPIStrayFlowKeys(test.line); !slices.Equal(got, test.want) {
			t.Errorf("openAPIStrayFlowKeys(%q) = %q, want %q", test.line, got, test.want)
		}
	}
}

// Every contract file must parse without stray null-valued keys: an unquoted
// flow description with a comma, or its block-style copy in openapi.yaml.
func TestOpenAPIHasNoStrayNullKeys(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "openapi", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("list the OpenAPI files: %v (%d found)", err, len(files))
	}
	for _, path := range files {
		for number, line := range strings.Split(readSourceFile(t, path), "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), ": null") {
				t.Errorf("%s:%d has a null-valued key: %s", filepath.Base(path), number+1, strings.TrimSpace(line))
			}
			for _, stray := range openAPIStrayFlowKeys(line) {
				t.Errorf("%s:%d reads %q as a stray key; quote the value", filepath.Base(path), number+1, stray)
			}
		}
	}
}
