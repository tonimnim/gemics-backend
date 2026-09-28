package bracket

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// The single elimination emitter is tested as properties rather than against
// hardcoded graphs. A hardcoded 64 entry bracket is unreadable and, worse, it
// only proves the generator still does what it did yesterday. The properties
// below are the things a knockout draw must actually satisfy: everyone plays,
// nobody plays twice in a round, the tree narrows to one match, byes fall out of
// pruning rather than being carved in, and the same draw regenerates byte for
// byte months later.

// seFieldSizes is every entry count from 2 to 64, which covers every awkward
// non-power-of-two: one over a power of two (3, 5, 9, 17, 33), one under
// (7, 15, 31, 63), the half-full cases (6, 12, 24, 48) and the exact powers.
func seFieldSizes() []int {
	sizes := make([]int, 0, 63)
	for n := 2; n <= 64; n++ {
		sizes = append(sizes, n)
	}
	return sizes
}

func seDraw(n int, thirdPlace bool) DrawInput {
	entries := make([]DrawEntry, n)
	for i := range entries {
		entries[i] = DrawEntry{EntryID: fmt.Sprintf("entry-%02d", i+1), SeedKey: i + 1}
	}
	return DrawInput{
		Format:  SingleElimination,
		Entries: entries,
		Config:  Config{BestOf: 3, ThirdPlace: thirdPlace},
	}
}

// seRaw is the emitter's own output: full size, byes still in place.
func seRaw(t *testing.T, in DrawInput) []Node {
	t.Helper()
	nodes, err := emitSingleElimination(in)
	if err != nil {
		t.Fatalf("emitSingleElimination(%d entries): %v", len(in.Entries), err)
	}
	return nodes
}

// seFinished runs the same pipeline Emit runs, so the invariants asserted here
// are the ones the persistence layer will see.
func seFinished(t *testing.T, in DrawInput) Graph {
	t.Helper()
	graph := Rank(Prune(Graph{Format: in.Format, Nodes: seRaw(t, in)}))
	if err := Validate(graph); err != nil {
		t.Fatalf("Validate(%d entries, thirdPlace=%v): %v", len(in.Entries), in.Config.ThirdPlace, err)
	}
	return graph
}

func seSlotKey(slot Slot) string {
	switch slot.Kind {
	case SourceEntry:
		return "entry:" + slot.EntryID
	case SourceWinnerOf, SourceLoserOf:
		return slot.Kind.String() + ":" + slot.Src.String()
	}
	return slot.Kind.String()
}

// sePairKey identifies the two participants of a match irrespective of side, so
// a swapped duplicate still counts as a duplicate.
func sePairKey(node Node) string {
	home, away := seSlotKey(node.Home), seSlotKey(node.Away)
	if away < home {
		home, away = away, home
	}
	return home + " vs " + away
}

func seNodeAt(nodes []Node, ref LocalRef) (Node, bool) {
	for _, node := range nodes {
		if node.Ref == ref {
			return node, true
		}
	}
	return Node{}, false
}

// seEntryPlacements counts how often each entry id appears in an entry slot and
// records the round it was found in.
func seEntryPlacements(nodes []Node) map[string][]LocalRef {
	placements := make(map[string][]LocalRef)
	for _, node := range nodes {
		for _, slot := range []Slot{node.Home, node.Away} {
			if slot.Kind == SourceEntry {
				placements[slot.EntryID] = append(placements[slot.EntryID], node.Ref)
			}
		}
	}
	return placements
}

func seParent(ref LocalRef) LocalRef {
	return LocalRef{Bracket: ref.Bracket, Round: ref.Round + 1, Number: (ref.Number + 1) / 2}
}

// seMeetingRound walks two round-one matches up the tree until they collide,
// which is the round in which their occupants can first play each other.
func seMeetingRound(t *testing.T, left, right LocalRef) int {
	t.Helper()
	for step := 0; step < 64; step++ {
		if left == right {
			return left.Round
		}
		left, right = seParent(left), seParent(right)
	}
	t.Fatalf("%s and %s never converge", left, right)
	return 0
}

// seRoundOneRefOf finds the first round match holding an entry.
func seRoundOneRefOf(t *testing.T, nodes []Node, entryID string) LocalRef {
	t.Helper()
	for _, node := range nodes {
		if node.Ref.Round != 1 || node.Ref.Bracket != BracketMain {
			continue
		}
		if node.Home.EntryID == entryID || node.Away.EntryID == entryID {
			return node.Ref
		}
	}
	t.Fatalf("entry %s is not in round one", entryID)
	return LocalRef{}
}

// seExpectedBronze is the number of bronze matches that survive pruning. It is
// derived from the format, not from the implementation: a bronze match needs two
// semifinals that were actually played, and with three entries one semifinal is
// a walkover.
func seExpectedBronze(n int, thirdPlace bool) int {
	if thirdPlace && n >= 4 {
		return 1
	}
	return 0
}

// sePlayout plays every match in a finished graph and reports who won and who
// lost each one.
//
// Matches are played in Rank order, which is a valid topological order because
// Rank puts every match strictly above the matches it consumes, so a slot is
// always resolved before it is read. `homeWins` decides each result, which lets
// the same bracket be played twice: once with the draw going to form and once
// with every favourite beaten. A wiring assertion cannot tell a graph that is
// well formed from one that is actually playable; this can.
func sePlayout(t *testing.T, graph Graph, homeWins func(home, away string) bool) (winner, loser map[LocalRef]string) {
	t.Helper()
	winner = make(map[LocalRef]string, len(graph.Nodes))
	loser = make(map[LocalRef]string, len(graph.Nodes))

	ordered := make([]Node, len(graph.Nodes))
	copy(ordered, graph.Nodes)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Rank < ordered[j].Rank })

	occupant := func(slot Slot) (string, bool) {
		switch slot.Kind {
		case SourceEntry:
			return slot.EntryID, true
		case SourceWinnerOf:
			id, ok := winner[slot.Src]
			return id, ok
		case SourceLoserOf:
			id, ok := loser[slot.Src]
			return id, ok
		}
		return "", false
	}

	for _, node := range ordered {
		home, homeKnown := occupant(node.Home)
		away, awayKnown := occupant(node.Away)
		if !homeKnown || !awayKnown {
			t.Fatalf("%s (rank %d): reached before its sources resolved", node.Ref, node.Rank)
		}
		// Mirrors matches CHECK (home_entry_id <> away_entry_id). A graph that
		// can seat one entrant on both sides of a match cannot be stored at all.
		if home == away {
			t.Fatalf("%s: %s plays itself", node.Ref, home)
		}
		if homeWins(home, away) {
			winner[node.Ref], loser[node.Ref] = home, away
		} else {
			winner[node.Ref], loser[node.Ref] = away, home
		}
	}
	return winner, loser
}

// seTerminalMain is the match nothing consumes the winner of: the final.
func seTerminalMain(graph Graph) LocalRef {
	consumed := make(map[LocalRef]bool)
	for _, node := range graph.Nodes {
		for _, slot := range []Slot{node.Home, node.Away} {
			if slot.Kind == SourceWinnerOf {
				consumed[slot.Src] = true
			}
		}
	}
	var final LocalRef
	for _, node := range graph.Nodes {
		if node.Ref.Bracket == BracketMain && !consumed[node.Ref] {
			final = node.Ref
		}
	}
	return final
}

// Playing the draw out is the property the wiring exists to serve. A knockout
// tournament must eliminate exactly one entrant per match and leave exactly one
// unbeaten, and a bronze must be contested by the two people who actually lost
// the semifinals rather than merely being wired to the semifinal matches. Both
// result rules are played because a graph can be correct on one path through it
// and wrong on another.
func TestSingleEliminationPlaysOut(t *testing.T) {
	for _, n := range seFieldSizes() {
		for _, thirdPlace := range []bool{false, true} {
			in := seDraw(n, thirdPlace)
			position := make(map[string]int, n)
			for i, entry := range in.Entries {
				position[entry.EntryID] = i
			}
			rules := []struct {
				name     string
				homeWins func(home, away string) bool
			}{
				{"to form", func(home, away string) bool { return position[home] < position[away] }},
				{"every upset", func(home, away string) bool { return position[home] > position[away] }},
			}
			for _, rule := range rules {
				t.Run(fmt.Sprintf("n=%d/third=%v/%s", n, thirdPlace, rule.name), func(t *testing.T) {
					graph := seFinished(t, in)
					winner, loser := sePlayout(t, graph, rule.homeWins)

					final := seTerminalMain(graph)
					if final.zero() {
						t.Fatalf("no match decides the title")
					}
					champion := winner[final]

					losses := make(map[string]int, n)
					appearances := make(map[string]int, n)
					for _, node := range graph.Nodes {
						appearances[winner[node.Ref]]++
						appearances[loser[node.Ref]]++
						if node.Ref.Bracket == BracketMain {
							losses[loser[node.Ref]]++
						}
					}
					for _, entry := range in.Entries {
						if appearances[entry.EntryID] == 0 {
							t.Fatalf("%s was drawn but never played a match", entry.EntryID)
						}
						want := 1
						if entry.EntryID == champion {
							want = 0
						}
						if got := losses[entry.EntryID]; got != want {
							t.Fatalf("%s lost %d knockout matches, want %d", entry.EntryID, got, want)
						}
					}
					// Nobody may appear who was not drawn.
					if got := len(appearances); got != n {
						t.Fatalf("%d entrants appear in results, want %d", got, n)
					}

					for _, node := range graph.Nodes {
						if node.Ref.Bracket != BracketBronze {
							continue
						}
						finalNode, ok := seNodeAt(graph.Nodes, final)
						if !ok {
							t.Fatalf("no final at %s", final)
						}
						if finalNode.Home.Kind != SourceWinnerOf || finalNode.Away.Kind != SourceWinnerOf {
							t.Fatalf("%s: a draw with a bronze must have two semifinals", final)
						}
						beaten := map[string]bool{
							loser[finalNode.Home.Src]: true,
							loser[finalNode.Away.Src]: true,
						}
						if !beaten[winner[node.Ref]] || !beaten[loser[node.Ref]] {
							t.Fatalf("%s was played by %s and %s, want the beaten semifinalists %v",
								node.Ref, winner[node.Ref], loser[node.Ref], beaten)
						}
						// The player who lost the final is second, not third.
						if winner[node.Ref] == loser[final] || loser[node.Ref] == loser[final] {
							t.Fatalf("%s: the beaten finalist %s also played for third", node.Ref, loser[final])
						}
					}
				})
			}
		}
	}
}

// The suite above stops at 64 because that is where the awkward shapes are. The
// platform's real ceiling is MaxEntries, and a draw that far out has hundreds of
// byes for Prune to unwind, so the ends of the range are checked too.
func TestSingleEliminationValidatesToCapacity(t *testing.T) {
	for _, n := range []int{65, 127, 128, 129, 511, 512, 513, MaxEntries - 1, MaxEntries} {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				graph, err := Emit(in)
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
				if err := Validate(graph); err != nil {
					t.Fatalf("Validate: %v", err)
				}
				if got, want := len(graph.Nodes), n-1+seExpectedBronze(n, thirdPlace); got != want {
					t.Fatalf("match count = %d, want %d", got, want)
				}
				placements := seEntryPlacements(graph.Nodes)
				if got := len(placements); got != n {
					t.Fatalf("%d distinct entries placed, want %d", got, n)
				}
			})
		}
	}
}

func TestSingleEliminationRejectsDegenerateField(t *testing.T) {
	for _, n := range []int{0, 1} {
		in := seDraw(n, false)
		if _, err := emitSingleElimination(in); !errors.Is(err, ErrTooFewEntries) {
			t.Fatalf("%d entries: got %v, want ErrTooFewEntries", n, err)
		}
	}
}

// The emitted tree is full: size/2 matches in round one and half as many in each
// round after, down to a single final.
func TestSingleEliminationRawTreeShape(t *testing.T) {
	for _, n := range seFieldSizes() {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				nodes := seRaw(t, in)

				size := nextPowerOfTwo(n)
				rounds := seRoundCount(size)
				wantBronze := 0
				if thirdPlace && rounds >= 2 {
					wantBronze = 1
				}
				if got, want := len(nodes), size-1+wantBronze; got != want {
					t.Fatalf("match count = %d, want %d", got, want)
				}

				perRound := make(map[int]int)
				refs := make(map[LocalRef]bool)
				for _, node := range nodes {
					if refs[node.Ref] {
						t.Fatalf("duplicate ref %s", node.Ref)
					}
					refs[node.Ref] = true
					if node.Rank != 0 {
						t.Fatalf("%s: emitter set Rank=%d, that is Rank()'s job", node.Ref, node.Rank)
					}
					if node.GroupKey != "" {
						t.Fatalf("%s: unexpected group key %q", node.Ref, node.GroupKey)
					}
					if node.Activation != ActivationUnconditional || !node.ActSrc.zero() {
						t.Fatalf("%s: knockout matches are unconditional, got %q/%s", node.Ref, node.Activation, node.ActSrc)
					}
					if node.Ref.Bracket == BracketMain {
						perRound[node.Ref.Round]++
					}
				}
				for round := 1; round <= rounds; round++ {
					if got, want := perRound[round], size>>round; got != want {
						t.Fatalf("round %d has %d matches, want %d", round, got, want)
					}
				}
				if got := perRound[rounds+1]; got != 0 {
					t.Fatalf("round %d should not exist, has %d matches", rounds+1, got)
				}
				if _, ok := seNodeAt(nodes, LocalRef{Bracket: BracketMain, Round: rounds, Number: 1}); !ok {
					t.Fatalf("no final at round %d", rounds)
				}

				// Canonical order, so the fingerprint and every positional
				// assertion downstream is stable.
				sorted := make([]Node, len(nodes))
				copy(sorted, nodes)
				sortNodes(sorted)
				if !reflect.DeepEqual(nodes, sorted) {
					t.Fatalf("emitter output is not in canonical order")
				}
			})
		}
	}
}

// Round one holds the draw exactly as it arrived: every entry once, in seed
// order, with byes in the surplus positions and never two byes in one match.
func TestSingleEliminationRoundOnePlacement(t *testing.T) {
	for _, n := range seFieldSizes() {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			in := seDraw(n, false)
			nodes := seRaw(t, in)
			size := nextPowerOfTwo(n)

			byes := 0
			var roundOne []Node
			for _, node := range nodes {
				if node.Ref.Round != 1 {
					continue
				}
				roundOne = append(roundOne, node)
				for _, slot := range []Slot{node.Home, node.Away} {
					switch slot.Kind {
					case SourceEntry, SourceBye:
					default:
						t.Fatalf("%s: round one slot is %s, want an entry or a bye", node.Ref, slot.Kind)
					}
					if slot.Kind == SourceBye {
						byes++
					}
				}
				if node.Home.Kind == SourceBye && node.Away.Kind == SourceBye {
					t.Fatalf("%s: both sides are byes", node.Ref)
				}
			}
			if got, want := len(roundOne), size/2; got != want {
				t.Fatalf("round one has %d matches, want %d", got, want)
			}
			if got, want := byes, size-n; got != want {
				t.Fatalf("round one has %d byes, want %d", got, want)
			}

			placements := seEntryPlacements(nodes)
			for _, entry := range in.Entries {
				if got := len(placements[entry.EntryID]); got != 1 {
					t.Fatalf("entry %s appears in %d slots, want 1", entry.EntryID, got)
				}
			}
			if got, want := len(placements), n; got != want {
				t.Fatalf("%d distinct entries placed, want %d", got, want)
			}

			// The draw order is the draw: the first entry heads the bracket.
			first := roundOne[0]
			if first.Home.Kind != SourceEntry || first.Home.EntryID != in.Entries[0].EntryID {
				t.Fatalf("%s home = %s, want the first entry", first.Ref, seSlotKey(first.Home))
			}
			// The second entry heads the other half, so it cannot meet the first
			// before the final.
			wantSecond := 1
			if size >= 4 {
				wantSecond = size/4 + 1
			}
			secondRef := seRoundOneRefOf(t, nodes, in.Entries[1].EntryID)
			if secondRef.Number != wantSecond {
				t.Fatalf("second entry is in round one match %d, want %d", secondRef.Number, wantSecond)
			}
		})
	}
}

// Every round after the first is pure structure: two winner edges pointing at
// the pair of matches directly below it, each consumed exactly once.
func TestSingleEliminationTreeWiring(t *testing.T) {
	for _, n := range seFieldSizes() {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				nodes := seRaw(t, in)
				size := nextPowerOfTwo(n)
				rounds := seRoundCount(size)

				winnerConsumers := make(map[LocalRef][]LocalRef)
				loserConsumers := make(map[LocalRef][]LocalRef)
				for _, node := range nodes {
					for _, slot := range []Slot{node.Home, node.Away} {
						switch slot.Kind {
						case SourceWinnerOf:
							winnerConsumers[slot.Src] = append(winnerConsumers[slot.Src], node.Ref)
						case SourceLoserOf:
							loserConsumers[slot.Src] = append(loserConsumers[slot.Src], node.Ref)
						}
						if slot.Kind == SourceWinnerOf || slot.Kind == SourceLoserOf {
							if _, ok := seNodeAt(nodes, slot.Src); !ok {
								t.Fatalf("%s points at %s which does not exist", node.Ref, slot.Src)
							}
							if slot.Src.Round >= node.Ref.Round {
								t.Fatalf("%s consumes %s, which is not an earlier round", node.Ref, slot.Src)
							}
						}
					}
				}

				for round := 2; round <= rounds; round++ {
					for number := 1; number <= size>>round; number++ {
						ref := LocalRef{Bracket: BracketMain, Round: round, Number: number}
						node, ok := seNodeAt(nodes, ref)
						if !ok {
							t.Fatalf("%s missing", ref)
						}
						wantHome := winnerSlot(LocalRef{Bracket: BracketMain, Round: round - 1, Number: 2*number - 1})
						wantAway := winnerSlot(LocalRef{Bracket: BracketMain, Round: round - 1, Number: 2 * number})
						if node.Home != wantHome || node.Away != wantAway {
							t.Fatalf("%s = %s/%s, want %s/%s", ref,
								seSlotKey(node.Home), seSlotKey(node.Away), seSlotKey(wantHome), seSlotKey(wantAway))
						}
					}
				}

				// match_slots_one_consumer_uidx: at most one consumer per side.
				final := LocalRef{Bracket: BracketMain, Round: rounds, Number: 1}
				for _, node := range nodes {
					if got := len(winnerConsumers[node.Ref]); got > 1 {
						t.Fatalf("winner of %s feeds %d slots: %v", node.Ref, got, winnerConsumers[node.Ref])
					}
					if got := len(loserConsumers[node.Ref]); got > 1 {
						t.Fatalf("loser of %s feeds %d slots: %v", node.Ref, got, loserConsumers[node.Ref])
					}
					if node.Ref.Bracket != BracketMain {
						continue
					}
					wantWinnerConsumers := 1
					if node.Ref == final {
						wantWinnerConsumers = 0
					}
					if got := len(winnerConsumers[node.Ref]); got != wantWinnerConsumers {
						t.Fatalf("winner of %s has %d consumers, want %d", node.Ref, got, wantWinnerConsumers)
					}
				}

				// Loser edges exist only for a bronze match.
				for src, consumers := range loserConsumers {
					for _, consumer := range consumers {
						if consumer.Bracket != BracketBronze {
							t.Fatalf("%s consumes the loser of %s outside the bronze bracket", consumer, src)
						}
					}
				}
				if !thirdPlace && len(loserConsumers) != 0 {
					t.Fatalf("no third place match was asked for, but loser edges exist: %v", loserConsumers)
				}
			})
		}
	}
}

// No round ever pairs the same two participants twice, before or after pruning.
func TestSingleEliminationNoDuplicatePairings(t *testing.T) {
	for _, n := range seFieldSizes() {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				for label, nodes := range map[string][]Node{
					"raw":    seRaw(t, in),
					"pruned": seFinished(t, in).Nodes,
				} {
					seen := make(map[string]LocalRef)
					for _, node := range nodes {
						key := fmt.Sprintf("%s/R%d/%s", node.Ref.Bracket, node.Ref.Round, sePairKey(node))
						if previous, taken := seen[key]; taken {
							t.Fatalf("%s: %s repeats the pairing of %s (%s)", label, node.Ref, previous, sePairKey(node))
						}
						seen[key] = node.Ref
					}
				}
			})
		}
	}
}

// The pipeline the persistence layer runs: emit, prune, rank, validate.
func TestSingleEliminationPipelineInvariants(t *testing.T) {
	for _, n := range seFieldSizes() {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				graph := seFinished(t, in)

				// Every knockout match eliminates exactly one entrant, so a
				// pruned draw is always n-1 matches, plus a bronze when one
				// survives.
				wantCount := n - 1 + seExpectedBronze(n, thirdPlace)
				if got := len(graph.Nodes); got != wantCount {
					t.Fatalf("pruned match count = %d, want %d", got, wantCount)
				}

				bronze := 0
				aboveRoundOne := 0
				for _, node := range graph.Nodes {
					if node.Rank < 1 {
						t.Fatalf("%s: rank %d", node.Ref, node.Rank)
					}
					if node.Ref.Bracket == BracketBronze {
						bronze++
					}
					for _, slot := range []Slot{node.Home, node.Away} {
						switch slot.Kind {
						case SourceBye, SourceEmpty:
							t.Fatalf("%s: %s slot survived pruning", node.Ref, slot.Kind)
						case SourceEntry:
							if node.Ref.Round > 1 {
								aboveRoundOne++
							}
						}
					}
				}
				if bronze != seExpectedBronze(n, thirdPlace) {
					t.Fatalf("%d bronze matches, want %d", bronze, seExpectedBronze(n, thirdPlace))
				}

				// A bye is exactly an entrant who starts above round one, and a
				// draw has one per unused bracket position. Nobody gets two.
				if got, want := aboveRoundOne, nextPowerOfTwo(n)-n; got != want {
					t.Fatalf("%d entrants start above round one, want %d", got, want)
				}

				placements := seEntryPlacements(graph.Nodes)
				for _, entry := range in.Entries {
					if got := len(placements[entry.EntryID]); got != 1 {
						t.Fatalf("entry %s appears %d times after pruning, want 1", entry.EntryID, got)
					}
				}
				if got := len(placements); got != n {
					t.Fatalf("%d distinct entries after pruning, want %d", got, n)
				}

				// Exactly one match nothing consumes in the main bracket: the
				// final. (The bronze is also terminal but sits in its own
				// bracket.)
				consumed := make(map[LocalRef]bool)
				for _, node := range graph.Nodes {
					for _, slot := range []Slot{node.Home, node.Away} {
						if slot.Kind == SourceWinnerOf {
							consumed[slot.Src] = true
						}
					}
				}
				terminals := 0
				for _, node := range graph.Nodes {
					if node.Ref.Bracket == BracketMain && !consumed[node.Ref] {
						terminals++
					}
				}
				if terminals != 1 {
					t.Fatalf("%d matches decide the title, want exactly 1", terminals)
				}

				// Emit() must agree with the pipeline run by hand.
				emitted, err := Emit(in)
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
				if !reflect.DeepEqual(emitted.Nodes, graph.Nodes) {
					t.Fatalf("Emit disagrees with emit+prune+rank")
				}
				if emitted.Fingerprint != Fingerprint(graph) {
					t.Fatalf("fingerprint mismatch")
				}
			})
		}
	}
}

// Standard seeding keeps the top two apart until the final and the top four
// apart until the semifinals. Checked on full brackets, where every seed exists.
func TestSingleEliminationSeparatesTopSeeds(t *testing.T) {
	for _, n := range []int{2, 4, 8, 16, 32, 64} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			in := seDraw(n, false)
			nodes := seRaw(t, in)
			rounds := seRoundCount(n)

			ref := func(seed int) LocalRef {
				return seRoundOneRefOf(t, nodes, in.Entries[seed-1].EntryID)
			}
			if got := seMeetingRound(t, ref(1), ref(2)); got != rounds {
				t.Fatalf("seeds 1 and 2 meet in round %d, want the final (round %d)", got, rounds)
			}
			if n >= 4 {
				if got := seMeetingRound(t, ref(1), ref(4)); got != rounds-1 {
					t.Fatalf("seeds 1 and 4 meet in round %d, want %d", got, rounds-1)
				}
				if got := seMeetingRound(t, ref(2), ref(3)); got != rounds-1 {
					t.Fatalf("seeds 2 and 3 meet in round %d, want %d", got, rounds-1)
				}
			}
		})
	}
}

// Byes are not something the emitter arranges. With six entries the top two
// seeds draw bye positions and Prune deletes those matches, which is how they
// end up starting in round two.
func TestSingleEliminationByesFallOutOfPruning(t *testing.T) {
	in := seDraw(6, false)
	graph := seFinished(t, in)

	if got := len(graph.Nodes); got != 5 {
		t.Fatalf("six entries produced %d matches, want 5", got)
	}

	roundOne := make(map[int]bool)
	for _, node := range graph.Nodes {
		if node.Ref.Round == 1 {
			roundOne[node.Ref.Number] = true
		}
	}
	// Match numbers are deliberately not renumbered after pruning, so the
	// surviving first round matches keep their original numbers.
	if len(roundOne) != 2 || !roundOne[2] || !roundOne[4] {
		t.Fatalf("surviving round one matches = %v, want {2, 4}", roundOne)
	}

	for _, want := range []struct {
		ref   LocalRef
		entry string
	}{
		{LocalRef{Bracket: BracketMain, Round: 2, Number: 1}, in.Entries[0].EntryID},
		{LocalRef{Bracket: BracketMain, Round: 2, Number: 2}, in.Entries[1].EntryID},
	} {
		node, ok := seNodeAt(graph.Nodes, want.ref)
		if !ok {
			t.Fatalf("%s missing", want.ref)
		}
		if node.Home.Kind != SourceEntry || node.Home.EntryID != want.entry {
			t.Fatalf("%s home = %s, want entry %s advanced by a bye", want.ref, seSlotKey(node.Home), want.entry)
		}
		if node.Away.Kind != SourceWinnerOf {
			t.Fatalf("%s away = %s, want a winner edge", want.ref, seSlotKey(node.Away))
		}
	}

	for _, node := range graph.Nodes {
		if node.Ref.Round != 1 {
			continue
		}
		for _, slot := range []Slot{node.Home, node.Away} {
			if slot.EntryID == in.Entries[0].EntryID || slot.EntryID == in.Entries[1].EntryID {
				t.Fatalf("%s: a seed with a bye is still playing in round one", node.Ref)
			}
		}
	}
}

func TestSingleEliminationThirdPlace(t *testing.T) {
	t.Run("two entries have no semifinals", func(t *testing.T) {
		for _, nodes := range map[string][]Node{
			"raw":    seRaw(t, seDraw(2, true)),
			"pruned": seFinished(t, seDraw(2, true)).Nodes,
		} {
			for _, node := range nodes {
				if node.Ref.Bracket == BracketBronze {
					t.Fatalf("%s: a two entry draw has no third place match", node.Ref)
				}
			}
		}
	})

	t.Run("three entries prune the bronze away", func(t *testing.T) {
		// One semifinal is a walkover, so there is no second loser to play.
		raw := seRaw(t, seDraw(3, true))
		if _, ok := seNodeAt(raw, LocalRef{Bracket: BracketBronze, Round: 2, Number: 1}); !ok {
			t.Fatalf("the emitter should still emit the bronze; Prune decides its fate")
		}
		for _, node := range seFinished(t, seDraw(3, true)).Nodes {
			if node.Ref.Bracket == BracketBronze {
				t.Fatalf("%s survived a walkover semifinal", node.Ref)
			}
		}
	})

	t.Run("bronze consumes both semifinal losers", func(t *testing.T) {
		for _, n := range seFieldSizes() {
			if n < 4 {
				continue
			}
			in := seDraw(n, true)
			graph := seFinished(t, in)

			var bronze Node
			var final LocalRef
			consumed := make(map[LocalRef]bool)
			for _, node := range graph.Nodes {
				if node.Ref.Bracket == BracketBronze {
					bronze = node
				}
				for _, slot := range []Slot{node.Home, node.Away} {
					if slot.Kind == SourceWinnerOf {
						consumed[slot.Src] = true
					}
				}
			}
			for _, node := range graph.Nodes {
				if node.Ref.Bracket == BracketMain && !consumed[node.Ref] {
					final = node.Ref
				}
			}
			if bronze.Ref.Bracket != BracketBronze {
				t.Fatalf("n=%d: no bronze match", n)
			}
			if bronze.Home.Kind != SourceLoserOf || bronze.Away.Kind != SourceLoserOf {
				t.Fatalf("n=%d: bronze = %s/%s, want two loser edges", n, seSlotKey(bronze.Home), seSlotKey(bronze.Away))
			}
			if bronze.Home.Src == bronze.Away.Src {
				t.Fatalf("n=%d: bronze consumes the same match twice", n)
			}
			// The semifinals are exactly the two matches feeding the final.
			finalNode, ok := seNodeAt(graph.Nodes, final)
			if !ok {
				t.Fatalf("n=%d: no final", n)
			}
			semis := map[LocalRef]bool{finalNode.Home.Src: true, finalNode.Away.Src: true}
			if finalNode.Home.Kind != SourceWinnerOf || finalNode.Away.Kind != SourceWinnerOf {
				t.Fatalf("n=%d: final is fed by %s/%s", n, seSlotKey(finalNode.Home), seSlotKey(finalNode.Away))
			}
			if !semis[bronze.Home.Src] || !semis[bronze.Away.Src] {
				t.Fatalf("n=%d: bronze consumes %s and %s, want the two semifinals %v",
					n, bronze.Home.Src, bronze.Away.Src, semis)
			}
		}
	})

	t.Run("no bronze unless asked for", func(t *testing.T) {
		for _, n := range seFieldSizes() {
			for _, node := range seFinished(t, seDraw(n, false)).Nodes {
				if node.Ref.Bracket != BracketMain {
					t.Fatalf("n=%d: unexpected bracket %q", n, node.Ref.Bracket)
				}
				for _, slot := range []Slot{node.Home, node.Away} {
					if slot.Kind == SourceLoserOf {
						t.Fatalf("n=%d: %s has a loser edge without a third place match", n, node.Ref)
					}
				}
			}
		}
	})
}

// The same draw must regenerate identically, because a placement has to be
// defensible months later.
func TestSingleEliminationDeterministic(t *testing.T) {
	for _, n := range []int{2, 3, 5, 6, 7, 9, 11, 13, 16, 23, 32, 45, 63, 64} {
		for _, thirdPlace := range []bool{false, true} {
			t.Run(fmt.Sprintf("n=%d/third=%v", n, thirdPlace), func(t *testing.T) {
				in := seDraw(n, thirdPlace)
				first := seRaw(t, in)
				firstGraph, err := Emit(in)
				if err != nil {
					t.Fatalf("Emit: %v", err)
				}
				for run := 0; run < 5; run++ {
					again := seRaw(t, seDraw(n, thirdPlace))
					if !reflect.DeepEqual(first, again) {
						t.Fatalf("run %d differs from run 0", run)
					}
					againGraph, err := Emit(seDraw(n, thirdPlace))
					if err != nil {
						t.Fatalf("Emit run %d: %v", run, err)
					}
					if !reflect.DeepEqual(firstGraph, againGraph) {
						t.Fatalf("Emit run %d differs from run 0", run)
					}
					if againGraph.Fingerprint != firstGraph.Fingerprint {
						t.Fatalf("fingerprint changed between runs")
					}
				}
			})
		}
	}
}

// The entry order is the draw, so reordering it must produce a different
// tournament rather than being silently re-sorted.
func TestSingleEliminationHonoursEntryOrder(t *testing.T) {
	in := seDraw(8, false)
	reversed := seDraw(8, false)
	for i, j := 0, len(reversed.Entries)-1; i < j; i, j = i+1, j-1 {
		reversed.Entries[i], reversed.Entries[j] = reversed.Entries[j], reversed.Entries[i]
	}

	forward, err := Emit(in)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	backward, err := Emit(reversed)
	if err != nil {
		t.Fatalf("Emit reversed: %v", err)
	}
	if forward.Fingerprint == backward.Fingerprint {
		t.Fatalf("reversing the draw produced the same graph")
	}

	// The first entry of each input heads its own bracket.
	first, ok := seNodeAt(backward.Nodes, LocalRef{Bracket: BracketMain, Round: 1, Number: 1})
	if !ok {
		t.Fatalf("no opening match")
	}
	if first.Home.EntryID != reversed.Entries[0].EntryID {
		t.Fatalf("opening home = %s, want %s", seSlotKey(first.Home), reversed.Entries[0].EntryID)
	}
}
