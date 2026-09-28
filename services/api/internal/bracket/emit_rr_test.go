package bracket

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// These tests are deliberately property-based rather than golden. A round robin
// has a small number of things it must be true about — every pair meets the
// right number of times, nobody plays twice in a round, nobody is stuck at home
// every week — and those properties hold for every field size, whereas a
// hardcoded fixture list for one size proves nothing about the awkward ones.

func rrEntries(count int) []DrawEntry {
	entries := make([]DrawEntry, count)
	for position := range entries {
		entries[position] = DrawEntry{
			EntryID: fmt.Sprintf("entry-%03d", position+1),
			SeedKey: position + 1,
		}
	}
	return entries
}

func rrIDs(entries []DrawEntry) []string {
	ids := make([]string, len(entries))
	for position, entry := range entries {
		ids[position] = entry.EntryID
	}
	return ids
}

func rrEmit(t *testing.T, entries []DrawEntry, config Config) Graph {
	t.Helper()
	graph, err := Emit(DrawInput{Format: RoundRobin, Entries: entries, Config: config})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	// Emit has already run Prune and Rank, so this is the real gate: it is the
	// Go mirror of every match_slots constraint the graph will be stored under.
	if err := Validate(graph); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return graph
}

// rrPair keys an unordered meeting so home/away orientation does not hide a
// duplicate fixture.
func rrPair(home, away string) string {
	if home > away {
		home, away = away, home
	}
	return home + " v " + away
}

// rrSide names one side of a match. Ranging a map[string]Slot to visit both
// sides would make which side a failure blames vary between runs, which is the
// one thing this package's doc comment promises never happens.
type rrSide struct {
	name string
	slot Slot
}

func rrSides(node Node) []rrSide {
	return []rrSide{{name: "home", slot: node.Home}, {name: "away", slot: node.Away}}
}

func rrByBracket(nodes []Node) map[string][]Node {
	tables := make(map[string][]Node)
	for _, node := range nodes {
		tables[node.Ref.Bracket] = append(tables[node.Ref.Bracket], node)
	}
	return tables
}

func rrBracketNames(nodes []Node) []string {
	names := make([]string, 0, 4)
	for name := range rrByBracket(nodes) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// rrCheckTable asserts everything one round robin table must satisfy. legs is 1
// for a single round robin and 2 for a double.
func rrCheckTable(t *testing.T, nodes []Node, members []string, legs int, bracketName, groupKey string) {
	t.Helper()

	count := len(members)
	size := count + count%2 // the phantom rounds an odd field up
	wantRounds := legs * (size - 1)
	wantMatches := legs * count * (count - 1) / 2

	if len(nodes) != wantMatches {
		t.Fatalf("%s: %d matches, want %d for %d entrants over %d legs",
			bracketName, len(nodes), wantMatches, count, legs)
	}

	isMember := make(map[string]bool, count)
	for _, id := range members {
		isMember[id] = true
	}

	appearances := make(map[string]int, count)
	homeCount := make(map[string]int, count)
	awayCount := make(map[string]int, count)
	meetings := make(map[string]int, wantMatches)
	orientations := make(map[string]int, wantMatches*2)
	refs := make(map[LocalRef]bool, len(nodes))
	rounds := make(map[int][]Node, wantRounds)

	for _, node := range nodes {
		if node.Ref.Bracket != bracketName {
			t.Fatalf("%s: match %s is in the wrong bracket", bracketName, node.Ref)
		}
		if node.GroupKey != groupKey {
			t.Errorf("%s: match %s has group key %q, want %q", bracketName, node.Ref, node.GroupKey, groupKey)
		}
		// Every slot is an entry, so nothing depends on anything and the whole
		// fixture list is playable from the moment the draw is made.
		if node.Rank != 1 {
			t.Errorf("%s: match %s has rank %d, want 1", bracketName, node.Ref, node.Rank)
		}
		if node.Activation != ActivationUnconditional {
			t.Errorf("%s: match %s is conditional (%q)", bracketName, node.Ref, node.Activation)
		}
		if !node.ActSrc.zero() {
			t.Errorf("%s: match %s carries an activation source", bracketName, node.Ref)
		}
		for _, side := range rrSides(node) {
			if side.slot.Kind != SourceEntry {
				t.Fatalf("%s: match %s %s slot is %s, want entry", bracketName, node.Ref, side.name, side.slot.Kind)
			}
			if !isMember[side.slot.EntryID] {
				t.Fatalf("%s: match %s %s slot holds %q which is not in this table",
					bracketName, node.Ref, side.name, side.slot.EntryID)
			}
		}
		if node.Home.EntryID == node.Away.EntryID {
			t.Fatalf("%s: match %s pairs %s with itself", bracketName, node.Ref, node.Home.EntryID)
		}
		if refs[node.Ref] {
			t.Fatalf("%s: duplicate match reference %s", bracketName, node.Ref)
		}
		refs[node.Ref] = true

		appearances[node.Home.EntryID]++
		appearances[node.Away.EntryID]++
		homeCount[node.Home.EntryID]++
		awayCount[node.Away.EntryID]++
		meetings[rrPair(node.Home.EntryID, node.Away.EntryID)]++
		orientations[node.Home.EntryID+" at home v "+node.Away.EntryID]++
		rounds[node.Ref.Round] = append(rounds[node.Ref.Round], node)
	}

	// Every pair meets exactly as often as the format says, and no more.
	if len(meetings) != count*(count-1)/2 {
		t.Errorf("%s: %d distinct pairings, want %d", bracketName, len(meetings), count*(count-1)/2)
	}
	for pair, met := range meetings {
		if met != legs {
			t.Errorf("%s: %s meet %d times, want %d", bracketName, pair, met, legs)
		}
	}
	if legs == 2 {
		// A double round robin is the schedule played through twice with the
		// sides swapped, so each ordered fixture happens exactly once.
		if len(orientations) != count*(count-1) {
			t.Errorf("%s: %d distinct home/away fixtures, want %d",
				bracketName, len(orientations), count*(count-1))
		}
		for fixture, played := range orientations {
			if played != 1 {
				t.Errorf("%s: %s played %d times, want 1", bracketName, fixture, played)
			}
		}
	}

	for _, id := range members {
		if got, want := appearances[id], legs*(count-1); got != want {
			t.Errorf("%s: %s plays %d matches, want %d", bracketName, id, got, want)
		}
	}

	// Round shape: no gaps in the round numbering, and every round is a full
	// slate of floor(count/2) simultaneous matches.
	if len(rounds) != wantRounds {
		t.Errorf("%s: %d rounds, want %d", bracketName, len(rounds), wantRounds)
	}
	for round := 1; round <= wantRounds; round++ {
		slate := rounds[round]
		if len(slate) != count/2 {
			t.Errorf("%s: round %d has %d matches, want %d", bracketName, round, len(slate), count/2)
		}
		playing := make(map[string]bool, len(slate)*2)
		seenPairs := make(map[string]bool, len(slate))
		numbers := make(map[int]bool, len(slate))
		for _, node := range slate {
			for _, id := range []string{node.Home.EntryID, node.Away.EntryID} {
				if playing[id] {
					t.Errorf("%s: %s plays twice in round %d", bracketName, id, round)
				}
				playing[id] = true
			}
			pair := rrPair(node.Home.EntryID, node.Away.EntryID)
			if seenPairs[pair] {
				t.Errorf("%s: %s is scheduled twice in round %d", bracketName, pair, round)
			}
			seenPairs[pair] = true
			if node.Ref.Number < 1 || node.Ref.Number > size/2 {
				t.Errorf("%s: match number %d in round %d is outside 1..%d",
					bracketName, node.Ref.Number, round, size/2)
			}
			if numbers[node.Ref.Number] {
				t.Errorf("%s: match number %d repeats in round %d", bracketName, node.Ref.Number, round)
			}
			numbers[node.Ref.Number] = true
		}
	}

	// Home/away: nobody may take every fixture at home. A single round robin over
	// an even field has an odd number of matches per entrant so one fixture of
	// slack is unavoidable; every other case must come out exactly level.
	slack := 0
	if legs == 1 && count%2 == 0 {
		slack = 1
	}
	for _, id := range members {
		difference := homeCount[id] - awayCount[id]
		if difference < 0 {
			difference = -difference
		}
		if difference > slack {
			t.Errorf("%s: %s has %d home and %d away fixtures, imbalance %d exceeds %d",
				bracketName, id, homeCount[id], awayCount[id], difference, slack)
		}
		if count > 2 && (homeCount[id] == 0 || awayCount[id] == 0) {
			t.Errorf("%s: %s never plays on one side (%d home, %d away)",
				bracketName, id, homeCount[id], awayCount[id])
		}
	}
}

func TestRoundRobinSingleTable(t *testing.T) {
	// Every field size from the minimum to sixty-four, which covers all the
	// awkward non-powers-of-two and both parities at every scale.
	for count := 2; count <= 64; count++ {
		t.Run(fmt.Sprintf("entrants=%d", count), func(t *testing.T) {
			entries := rrEntries(count)
			graph := rrEmit(t, entries, Config{})
			if names := rrBracketNames(graph.Nodes); len(names) != 1 || names[0] != BracketMain {
				t.Fatalf("brackets %v, want just %q", names, BracketMain)
			}
			rrCheckTable(t, graph.Nodes, rrIDs(entries), 1, BracketMain, "")
		})
	}
}

// MaxEntries is 1024, so stopping the sweep at sixty-four leaves the entire
// upper half of the supported range unexercised. These sizes are picked to stay
// cheap: an odd field costs far more than an even one of the same size because
// every round hands Prune a bye match to remove from a quadratic node list.
func TestRoundRobinLargeFields(t *testing.T) {
	for _, count := range []int{65, 128, 129, 256} {
		t.Run(fmt.Sprintf("entrants=%d", count), func(t *testing.T) {
			entries := rrEntries(count)
			graph := rrEmit(t, entries, Config{})
			rrCheckTable(t, graph.Nodes, rrIDs(entries), 1, BracketMain, "")
		})
	}
	t.Run("entrants=64/groups=8/double", func(t *testing.T) {
		entries := rrEntries(64)
		graph := rrEmit(t, entries, Config{GroupCount: 8, DoubleRoundRobin: true})
		if names := rrBracketNames(graph.Nodes); len(names) != 8 {
			t.Fatalf("brackets %v, want eight", names)
		}
	})
}

// DrawEntry carries a GroupKey that this emitter does not read: groups come from
// position in the draw vector, never from the entry. Pinning that here so the
// silence is a decision rather than an oversight, and so anyone wiring organizer
// chosen groups later finds the place they have to change.
func TestRoundRobinIgnoresDrawEntryGroupKey(t *testing.T) {
	entries := rrEntries(8)
	for position := range entries {
		entries[position].GroupKey = "organizer-choice"
	}
	graph := rrEmit(t, entries, Config{GroupCount: 2})
	for _, node := range graph.Nodes {
		if node.GroupKey == "organizer-choice" {
			t.Fatalf("match %s took its group from DrawEntry.GroupKey", node.Ref)
		}
		if node.GroupKey != node.Ref.Bracket {
			t.Fatalf("match %s has group key %q, want its bracket %q",
				node.Ref, node.GroupKey, node.Ref.Bracket)
		}
	}
}

func TestRoundRobinDoubleTable(t *testing.T) {
	for count := 2; count <= 40; count++ {
		t.Run(fmt.Sprintf("entrants=%d", count), func(t *testing.T) {
			entries := rrEntries(count)
			graph := rrEmit(t, entries, Config{DoubleRoundRobin: true})
			rrCheckTable(t, graph.Nodes, rrIDs(entries), 2, BracketMain, "")
		})
	}
}

// The second leg must be the first leg mirrored, fixture for fixture, and must
// continue the round numbering rather than restarting it.
func TestRoundRobinSecondLegMirrorsTheFirst(t *testing.T) {
	for _, count := range []int{2, 3, 4, 5, 8, 11, 16} {
		t.Run(fmt.Sprintf("entrants=%d", count), func(t *testing.T) {
			entries := rrEntries(count)
			single := rrEmit(t, entries, Config{})
			double := rrEmit(t, entries, Config{DoubleRoundRobin: true})

			size := count + count%2
			legRounds := size - 1

			firstLeg := make(map[string]bool)
			secondLeg := make(map[string]bool)
			for _, node := range double.Nodes {
				fixture := node.Home.EntryID + ">" + node.Away.EntryID
				if node.Ref.Round <= legRounds {
					firstLeg[fixture] = true
					continue
				}
				if node.Ref.Round > 2*legRounds {
					t.Fatalf("match %s is past the last round %d", node.Ref, 2*legRounds)
				}
				secondLeg[fixture] = true
			}
			if len(firstLeg) != len(secondLeg) {
				t.Fatalf("legs hold %d and %d fixtures", len(firstLeg), len(secondLeg))
			}
			for fixture := range firstLeg {
				home, away, _ := rrSplitFixture(fixture)
				if !secondLeg[away+">"+home] {
					t.Errorf("second leg never returns %s at %s", home, away)
				}
			}

			// The opening leg of a double round robin is the single schedule, and
			// not merely the same set of fixtures: it must be the same match in the
			// same round with the same number and the same sides, or a stored draw
			// could not be compared against a regenerated one. The mirror must then
			// sit exactly one leg later, so round release plays a whole first leg
			// before any return fixture.
			byRef := make(map[LocalRef]Node, len(double.Nodes))
			for _, node := range double.Nodes {
				byRef[node.Ref] = node
			}
			for _, node := range single.Nodes {
				opening, present := byRef[node.Ref]
				if !present {
					t.Fatalf("single-leg match %s is missing from the double schedule", node.Ref)
				}
				if opening.Home.EntryID != node.Home.EntryID || opening.Away.EntryID != node.Away.EntryID {
					t.Errorf("%s is %s v %s single but %s v %s double", node.Ref,
						node.Home.EntryID, node.Away.EntryID, opening.Home.EntryID, opening.Away.EntryID)
				}
				mirrorRef := LocalRef{
					Bracket: node.Ref.Bracket,
					Round:   node.Ref.Round + legRounds,
					Number:  node.Ref.Number,
				}
				mirror, present := byRef[mirrorRef]
				if !present {
					t.Fatalf("%s has no return fixture at %s", node.Ref, mirrorRef)
				}
				if mirror.Home.EntryID != node.Away.EntryID || mirror.Away.EntryID != node.Home.EntryID {
					t.Errorf("return of %s is %s v %s, want the reverse %s v %s", node.Ref,
						mirror.Home.EntryID, mirror.Away.EntryID, node.Away.EntryID, node.Home.EntryID)
				}
			}
		})
	}
}

func rrSplitFixture(fixture string) (home, away string, ok bool) {
	for position := 0; position < len(fixture); position++ {
		if fixture[position] == '>' {
			return fixture[:position], fixture[position+1:], true
		}
	}
	return "", "", false
}

func TestRoundRobinGroups(t *testing.T) {
	cases := []struct {
		count  int
		groups int
		double bool
	}{
		{count: 4, groups: 2},
		{count: 6, groups: 2},
		{count: 7, groups: 2},
		{count: 8, groups: 2, double: true},
		{count: 9, groups: 3},
		{count: 10, groups: 3},
		{count: 11, groups: 3},
		{count: 12, groups: 4},
		{count: 13, groups: 4},
		{count: 16, groups: 4, double: true},
		{count: 24, groups: 6},
		{count: 32, groups: 8},
		{count: 33, groups: 8},
		{count: 64, groups: 8},
	}
	for _, testCase := range cases {
		name := fmt.Sprintf("entrants=%d/groups=%d/double=%v", testCase.count, testCase.groups, testCase.double)
		t.Run(name, func(t *testing.T) {
			entries := rrEntries(testCase.count)
			graph := rrEmit(t, entries, Config{
				GroupCount:       testCase.groups,
				DoubleRoundRobin: testCase.double,
			})

			legs := 1
			if testCase.double {
				legs = 2
			}
			tables := rrByBracket(graph.Nodes)
			if len(tables) != testCase.groups {
				t.Fatalf("%d brackets, want %d: %v", len(tables), testCase.groups, rrBracketNames(graph.Nodes))
			}

			smallest, largest := testCase.count, 0
			for group := 0; group < testCase.groups; group++ {
				bracketName := GroupBracketPrefix + groupLabel(group)
				nodes, present := tables[bracketName]
				if !present {
					t.Fatalf("no matches in bracket %q", bracketName)
				}
				// Dealt in order: group g takes entries g, g+groups, g+2*groups.
				var members []string
				for position := group; position < testCase.count; position += testCase.groups {
					members = append(members, entries[position].EntryID)
				}
				if len(members) < smallest {
					smallest = len(members)
				}
				if len(members) > largest {
					largest = len(members)
				}
				rrCheckTable(t, nodes, members, legs, bracketName, bracketName)
			}
			if largest-smallest > 1 {
				t.Errorf("group sizes range from %d to %d, want them within one", smallest, largest)
			}
		})
	}
}

func TestRoundRobinDealsSeedsAcrossGroups(t *testing.T) {
	entries := rrEntries(12)
	graph := rrEmit(t, entries, Config{GroupCount: 4})

	want := map[string][]string{
		"group_a": {"entry-001", "entry-005", "entry-009"},
		"group_b": {"entry-002", "entry-006", "entry-010"},
		"group_c": {"entry-003", "entry-007", "entry-011"},
		"group_d": {"entry-004", "entry-008", "entry-012"},
	}
	got := make(map[string]map[string]bool)
	for _, node := range graph.Nodes {
		if got[node.Ref.Bracket] == nil {
			got[node.Ref.Bracket] = make(map[string]bool)
		}
		got[node.Ref.Bracket][node.Home.EntryID] = true
		got[node.Ref.Bracket][node.Away.EntryID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("brackets %v, want four groups", rrBracketNames(graph.Nodes))
	}
	for bracketName, members := range want {
		found := got[bracketName]
		if len(found) != len(members) {
			t.Errorf("%s holds %d entrants, want %d", bracketName, len(found), len(members))
		}
		for _, id := range members {
			if !found[id] {
				t.Errorf("%s does not hold %s", bracketName, id)
			}
		}
	}
}

// A group count the field cannot fill would hand somebody an empty schedule, so
// it is capped instead of honoured literally.
func TestRoundRobinCapsGroupsToTheField(t *testing.T) {
	cases := []struct {
		count      int
		configured int
		want       int
	}{
		{count: 2, configured: 0, want: 1},
		{count: 2, configured: 1, want: 1},
		{count: 2, configured: 4, want: 1},
		{count: 3, configured: 2, want: 1},
		{count: 3, configured: -5, want: 1},
		{count: 4, configured: 2, want: 2},
		{count: 5, configured: 3, want: 2},
		{count: 6, configured: 3, want: 3},
		{count: 7, configured: 8, want: 3},
		{count: 16, configured: 8, want: 8},
		{count: 16, configured: 64, want: 8},
	}
	for _, testCase := range cases {
		name := fmt.Sprintf("entrants=%d/configured=%d", testCase.count, testCase.configured)
		t.Run(name, func(t *testing.T) {
			entries := rrEntries(testCase.count)
			graph := rrEmit(t, entries, Config{GroupCount: testCase.configured})
			names := rrBracketNames(graph.Nodes)
			if len(names) != testCase.want {
				t.Fatalf("brackets %v, want %d", names, testCase.want)
			}
			if testCase.want == 1 && names[0] != BracketMain {
				t.Errorf("single table is in bracket %q, want %q", names[0], BracketMain)
			}
			// Whatever the cap does, every entrant must still have a schedule.
			playing := make(map[string]bool, testCase.count)
			for _, node := range graph.Nodes {
				playing[node.Home.EntryID] = true
				playing[node.Away.EntryID] = true
			}
			for _, entry := range entries {
				if !playing[entry.EntryID] {
					t.Errorf("%s has no fixtures at all", entry.EntryID)
				}
			}

			// Counting brackets is not enough. A cap that produced the right number
			// of tables but dealt them badly, or scheduled one of them wrongly, would
			// pass everything above, so every resulting table gets the full check.
			tables := rrByBracket(graph.Nodes)
			for group := 0; group < testCase.want; group++ {
				bracketName, groupKey := BracketMain, ""
				if testCase.want > 1 {
					bracketName = GroupBracketPrefix + groupLabel(group)
					groupKey = bracketName
				}
				nodes, present := tables[bracketName]
				if !present {
					t.Fatalf("no matches in bracket %q", bracketName)
				}
				var members []string
				for position := group; position < testCase.count; position += testCase.want {
					members = append(members, entries[position].EntryID)
				}
				// The cap exists precisely so this cannot be one.
				if len(members) < 2 {
					t.Fatalf("%s was dealt %d entrants: the cap failed", bracketName, len(members))
				}
				rrCheckTable(t, nodes, members, 1, bracketName, groupKey)
			}
		})
	}
}

func TestRoundRobinSingleTableCarriesNoGroupKey(t *testing.T) {
	graph := rrEmit(t, rrEntries(7), Config{})
	for _, node := range graph.Nodes {
		if node.GroupKey != "" {
			t.Fatalf("match %s carries group key %q with no groups configured", node.Ref, node.GroupKey)
		}
	}
}

// The emitter is naive about byes on purpose: it lays out the full even-sized
// circle and marks the phantom's fixture, and Prune is the only thing that
// removes it. This asserts that division of labour directly.
func TestRoundRobinEmitsByesForPruneToRemove(t *testing.T) {
	for count := 2; count <= 33; count++ {
		t.Run(fmt.Sprintf("entrants=%d", count), func(t *testing.T) {
			nodes, err := emitRoundRobin(DrawInput{Format: RoundRobin, Entries: rrEntries(count)})
			if err != nil {
				t.Fatalf("emitRoundRobin: %v", err)
			}

			size := count + count%2
			rounds := size - 1
			if want := rounds * (size / 2); len(nodes) != want {
				t.Fatalf("%d raw matches, want the full circle of %d", len(nodes), want)
			}

			byes := 0
			for _, node := range nodes {
				for _, side := range rrSides(node) {
					switch side.slot.Kind {
					case SourceEntry, SourceBye:
					default:
						t.Fatalf("match %s %s slot is %s: a round robin has no edges",
							node.Ref, side.name, side.slot.Kind)
					}
					if side.slot.Kind == SourceBye {
						byes++
					}
				}
				if node.Home.Kind == SourceBye && node.Away.Kind == SourceBye {
					t.Errorf("match %s is two byes", node.Ref)
				}
			}

			// An odd field sits exactly one entrant out per round; an even field
			// has nobody sitting out at all.
			want := 0
			if count%2 == 1 {
				want = rounds
			}
			if byes != want {
				t.Errorf("%d bye slots, want %d", byes, want)
			}

			// WHERE the bye sits is the actual contract with Prune. The phantom
			// occupies the pinned circle position, so it is always column zero and
			// therefore always match number 1, and that is what makes the surviving
			// gap in match_number the predictable one Prune's comment refuses to
			// renumber away. Counting byes alone would not notice a phantom that
			// wandered between columns.
			byeRounds := make(map[int]int, rounds)
			for _, node := range nodes {
				if node.Home.Kind != SourceBye && node.Away.Kind != SourceBye {
					continue
				}
				if node.Ref.Number != 1 {
					t.Errorf("bye at %s: the phantom must sit at match number 1", node.Ref)
				}
				byeRounds[node.Ref.Round]++
			}
			if count%2 == 1 {
				for round := 1; round <= rounds; round++ {
					if byeRounds[round] != 1 {
						t.Errorf("round %d has %d byes, want exactly one entrant sitting out",
							round, byeRounds[round])
					}
				}
			}

			pruned := Rank(Prune(Graph{Format: RoundRobin, Nodes: nodes}))
			if err := Validate(pruned); err != nil {
				t.Errorf("after Prune and Rank: %v", err)
			}
			// The gap Prune leaves behind: for an odd field no round keeps a match
			// number 1, and for an even field every round still has one.
			for _, node := range pruned.Nodes {
				if count%2 == 1 && node.Ref.Number == 1 {
					t.Errorf("%s survived pruning: match number 1 is the phantom's", node.Ref)
				}
			}
			if count%2 == 0 {
				survivors := 0
				for _, node := range pruned.Nodes {
					if node.Ref.Number == 1 {
						survivors++
					}
				}
				if survivors != rounds {
					t.Errorf("%d rounds keep a match number 1, want %d: an even field has no phantom",
						survivors, rounds)
				}
			}
		})
	}
}

// The one structural promise of this format: no winner_of or loser_of edges at
// all, so match_slots holds only entry rows and nothing can violate
// match_slots_one_consumer_uidx or the acyclicity check.
func TestRoundRobinHasNoEdges(t *testing.T) {
	configs := []Config{
		{},
		{DoubleRoundRobin: true},
		{GroupCount: 4},
		{GroupCount: 3, DoubleRoundRobin: true},
	}
	for index, config := range configs {
		t.Run(fmt.Sprintf("config=%d", index), func(t *testing.T) {
			graph := rrEmit(t, rrEntries(17), config)
			if len(graph.Nodes) == 0 {
				t.Fatal("no matches generated")
			}
			for _, node := range graph.Nodes {
				for _, side := range rrSides(node) {
					if side.slot.Kind != SourceEntry {
						t.Fatalf("match %s %s slot is %s", node.Ref, side.name, side.slot.Kind)
					}
					if !side.slot.Src.zero() {
						t.Errorf("match %s %s slot names a source match", node.Ref, side.name)
					}
				}
				if node.Rank != 1 {
					t.Errorf("match %s has rank %d: every round robin match is immediately playable",
						node.Ref, node.Rank)
				}
			}
		})
	}
}

func TestRoundRobinIsDeterministic(t *testing.T) {
	cases := []struct {
		count  int
		config Config
	}{
		{count: 2, config: Config{}},
		{count: 7, config: Config{}},
		{count: 16, config: Config{}},
		{count: 9, config: Config{DoubleRoundRobin: true}},
		{count: 23, config: Config{GroupCount: 4}},
		{count: 30, config: Config{GroupCount: 6, DoubleRoundRobin: true}},
	}
	for _, testCase := range cases {
		name := fmt.Sprintf("entrants=%d/groups=%d/double=%v",
			testCase.count, testCase.config.GroupCount, testCase.config.DoubleRoundRobin)
		t.Run(name, func(t *testing.T) {
			entries := rrEntries(testCase.count)
			first := rrEmit(t, entries, testCase.config)
			for run := 0; run < 16; run++ {
				again := rrEmit(t, rrEntries(testCase.count), testCase.config)
				if again.Fingerprint != first.Fingerprint {
					t.Fatalf("run %d produced fingerprint %x, want %x",
						run, again.Fingerprint, first.Fingerprint)
				}
				if !reflect.DeepEqual(again.Nodes, first.Nodes) {
					t.Fatalf("run %d produced a different node list", run)
				}
			}
		})
	}
}

// Emit must hand back nodes already in canonical order, because Fingerprint and
// every test that reads by position depend on it.
func TestRoundRobinIsCanonicallyOrdered(t *testing.T) {
	graph := rrEmit(t, rrEntries(21), Config{GroupCount: 4, DoubleRoundRobin: true})
	for position := 1; position < len(graph.Nodes); position++ {
		previous, current := graph.Nodes[position-1].Ref, graph.Nodes[position].Ref
		if !previous.less(current) {
			t.Fatalf("match %s is not before %s in canonical order", previous, current)
		}
	}
}

// The input vector is the draw. Reordering it must change the schedule, and
// feeding the same order back must reproduce it exactly.
func TestRoundRobinRespectsTheDrawOrder(t *testing.T) {
	entries := rrEntries(8)
	forward := rrEmit(t, entries, Config{})

	reversed := make([]DrawEntry, len(entries))
	for position, entry := range entries {
		reversed[len(entries)-1-position] = entry
	}
	backward := rrEmit(t, reversed, Config{})

	if forward.Fingerprint == backward.Fingerprint {
		t.Fatal("reversing the draw produced an identical schedule, so the order was ignored")
	}

	repeat := rrEmit(t, rrEntries(8), Config{})
	if repeat.Fingerprint != forward.Fingerprint {
		t.Fatal("the same draw order produced a different schedule")
	}
}

func TestRoundRobinRejectsTooFewEntries(t *testing.T) {
	for _, entries := range [][]DrawEntry{nil, rrEntries(1)} {
		if _, err := emitRoundRobin(DrawInput{Format: RoundRobin, Entries: entries}); !errors.Is(err, ErrTooFewEntries) {
			t.Fatalf("emitRoundRobin with %d entries returned %v, want ErrTooFewEntries", len(entries), err)
		}
	}
}

func TestRoundRobinGroupLabel(t *testing.T) {
	cases := []struct {
		group int
		want  string
	}{
		{group: 0, want: "a"},
		{group: 1, want: "b"},
		{group: 25, want: "z"},
		{group: 26, want: "aa"},
		{group: 27, want: "ab"},
		{group: 51, want: "az"},
		{group: 52, want: "ba"},
		{group: 701, want: "zz"},
		{group: 702, want: "aaa"},
	}
	for _, testCase := range cases {
		if got := groupLabel(testCase.group); got != testCase.want {
			t.Errorf("groupLabel(%d) = %q, want %q", testCase.group, got, testCase.want)
		}
	}
	// Labels must be distinct, or two groups would share a bracket name and
	// collide under UNIQUE(stage_id, bracket, round_number, match_number).
	seen := make(map[string]int, 1024)
	for group := 0; group < 1024; group++ {
		label := groupLabel(group)
		if previous, taken := seen[label]; taken {
			t.Fatalf("groups %d and %d both label as %q", previous, group, label)
		}
		seen[label] = group
	}
}

func TestRoundRobinCapacityMatchesTheEmitter(t *testing.T) {
	for count := 2; count <= 40; count++ {
		for configured := 0; configured <= 6; configured++ {
			for _, double := range []bool{false, true} {
				groups := roundRobinGroups(configured, count)
				nodes, err := emitRoundRobin(DrawInput{
					Format:  RoundRobin,
					Entries: rrEntries(count),
					Config:  Config{GroupCount: configured, DoubleRoundRobin: double},
				})
				if err != nil {
					t.Fatalf("entrants=%d groups=%d: %v", count, configured, err)
				}
				if got := roundRobinCapacity(count, groups, double); got != len(nodes) {
					t.Errorf("entrants=%d groups=%d double=%v: capacity %d, emitted %d",
						count, configured, double, got, len(nodes))
				}
			}
		}
	}
}
