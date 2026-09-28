package bracket

// Double elimination.
//
// The graph is three connected pieces.
//
//	winners      A single elimination tree over nextPowerOfTwo(len(entries))
//	             positions, seeded with seedOrder, in bracket BracketWinners.
//	             Identical in shape to the single elimination emitter; only the
//	             bracket name differs, and every match additionally exposes its
//	             loser to the losers bracket.
//
//	losers       For a winners bracket of size 2^k the losers bracket has
//	             2k-2 rounds. Odd rounds are MINOR: everyone in them is already
//	             carrying a loss and they play each other. Even rounds are
//	             MAJOR: the minor round's survivors meet the entrants dropping
//	             out of the winners bracket. Round 1 is the degenerate minor
//	             round where the "survivors" are the winners round 1 losers
//	             themselves.
//
//	grand_final  Two matches. The first is the winners bracket champion against
//	             the losers bracket champion. The second is the bracket reset,
//	             which exists in the graph from draw time but only becomes
//	             playable if the losers bracket champion wins the first.
//
// # Sizes
//
// With rounds = k and a winners bracket of size 2^k:
//
//	winners round r          2^(k-r) matches, r = 1..k
//	losers round 1           2^(k-2) matches
//	losers round 2j          2^(k-1-j) matches, j = 1..k-1   (major)
//	losers round 2j+1        2^(k-2-j) matches, j = 1..k-2   (minor)
//
// which totals 2^k - 1 winners matches and 2^k - 2 losers matches. Adding the
// two grand finals and letting Prune strip the byes leaves exactly 2n-1 matches
// for n entrants, the arithmetic being that every one of the n-1 eliminated
// players absorbs two losses, each played match produces exactly one loss, and
// the reset is the one match that may go unplayed.
//
// k = 1 (a two entrant draw) has no losers bracket at all. The losing finalist
// is already the losers bracket champion, so the grand final takes them via
// loserSlot of the winners final. That is one of the two same-source pairs
// match_slots_one_consumer_uidx is deliberately keyed to allow.
//
// # The drop mapping
//
// Major round 2j pairs the winner of minor round 2j-1 match m against the loser
// of winners round j+1 match dropIndex(j, m). Choosing dropIndex is the whole
// difficulty of the format: the naive identity mapping guarantees an immediate
// rematch.
//
// The mapping used here is
//
//	dropIndex(j, m) = width+1-m   when j is odd     (reversal)
//	dropIndex(j, m) = m           when j is even    (identity)
//
// where width = 2^(k-1-j) is the number of matches in both losers round 2j and
// winners round j+1. Alternating rather than always reversing is the point;
// always reversing is a well known way to reintroduce the rematch from round 4
// onwards.
//
// Why it works. Let D be the loser of winners round j+1 match t. In winners
// round j, D won match q, where q is 2t-1 or 2t, and the player D beat there is
// V, the loser of that match.
//
//	j = 1. V is a winners round 1 loser, so V sits in losers round 1 match
//	ceil(q/2) = t, and the winner of losers round 1 match m plays in losers
//	round 2 match m. D lands in match dropIndex(1, m) = width+1-m, so D and V
//	share a match only if width+1-m = m. width is a power of two, so width+1 is
//	odd and that equation has no integer solution unless width = 1.
//
//	j >= 2. V drops into major round 2j-2 at the match m'' with
//	dropIndex(j-1, m'') = q, then, winning, into minor round 2j-1 match
//	ceil(m''/2), then into major round 2j match ceil(m''/2). Substituting both
//	branches of the alternation gives the same answer either way:
//
//	  j even, so dropIndex(j, m) = m, t = m, q in {2m-1, 2m}, and dropIndex(j-1)
//	  is the reversal on 2*width, so m'' is 2*width+2-2m or 2*width+1-2m and
//	  ceil(m''/2) = width+1-m.
//
//	  j odd, so dropIndex(j, m) = width+1-m, t = width+1-m, q in
//	  {2*width+1-2m, 2*width+2-2m}, and dropIndex(j-1) is the identity, so
//	  m'' = q and again ceil(m''/2) = width+1-m.
//
//	So V always arrives at major round 2j match width+1-m while D is at match m,
//	and width+1-m = m has no solution for width > 1.
//
// The one exemption is width = 1, a major round with a single match. That is
// always the last major round, round 2k-2, where the losers bracket final must
// take the losers bracket survivor against the beaten winners finalist and
// there is no other match to send either of them to. It is also round 2 of a
// four entrant draw. In both cases the rematch is forced by the format, not by
// this mapping.
//
// This says nothing about rematches from two or more winners rounds back: those
// are unavoidable in a losers bracket that is a plain binary tree, and no
// mapping of the drops can prevent them. See emit_de_test.go, which asserts the
// immediate case by simulation and does not claim the general one.
//
// # The bracket reset and match_slots_one_consumer_uidx
//
// The index says a match's winner may feed at most one slot and its loser at
// most one slot. The first grand final is a leaf: nothing else in the graph
// consumes either of its outcomes. The reset can therefore take both, home from
// loserSlot and away from winnerSlot, spending each exactly once.
//
// The sides are chosen so that across both grand finals home is always the
// player who arrived through the winners bracket. The reset only exists when
// the away side won the first grand final, so at that moment the first grand
// final's loser is the winners bracket finalist and its winner is the losers
// bracket finalist, which is precisely the swap written below. Reading
// loserSlot as home looks odd in isolation and is the reason for this
// paragraph.
//
// The generator is pure: no clock, no randomness, no map iteration.
func emitDoubleElimination(in DrawInput) ([]Node, error) {
	if len(in.Entries) < 2 {
		return nil, ErrTooFewEntries
	}
	size := nextPowerOfTwo(len(in.Entries))
	rounds := deRoundCount(size)

	nodes := make([]Node, 0, 2*size)
	nodes = deAppendWinners(nodes, in.Entries, size, rounds)
	nodes = deAppendLosers(nodes, size, rounds)
	nodes = deAppendFinals(nodes, rounds)

	sortNodes(nodes)
	return nodes, nil
}

func deWinnersRef(round, number int) LocalRef {
	return LocalRef{Bracket: BracketWinners, Round: round, Number: number}
}

func deLosersRef(round, number int) LocalRef {
	return LocalRef{Bracket: BracketLosers, Round: round, Number: number}
}

// deFinalRef numbers the two grand finals as separate rounds rather than as two
// matches of one round, because the reset is played after the first final and
// round_number is the ordering the schedule reads.
func deFinalRef(round int) LocalRef {
	return LocalRef{Bracket: BracketGrandFinal, Round: round, Number: 1}
}

// deRoundCount is log2 of a power of two, expressed as the number of rounds a
// bracket of that size takes.
func deRoundCount(size int) int {
	rounds := 0
	for width := size; width > 1; width >>= 1 {
		rounds++
	}
	return rounds
}

// deSeatSlot fills a 1-based seeding position from the ordered draw. The order
// of in.Entries is the draw and is never rearranged here; seedOrder only says
// which bracket position each draw position occupies. Positions beyond the
// field are byes, which Prune removes afterwards.
func deSeatSlot(entries []DrawEntry, position int) Slot {
	if position < 1 || position > len(entries) {
		return byeSlot()
	}
	return entrySlot(entries[position-1].EntryID)
}

func deAppendWinners(nodes []Node, entries []DrawEntry, size, rounds int) []Node {
	order := seedOrder(size)
	for number := 1; number <= size/2; number++ {
		nodes = append(nodes, Node{
			Ref:        deWinnersRef(1, number),
			Home:       deSeatSlot(entries, order[2*number-2]),
			Away:       deSeatSlot(entries, order[2*number-1]),
			Activation: ActivationUnconditional,
		})
	}
	for round := 2; round <= rounds; round++ {
		for number := 1; number <= size>>round; number++ {
			nodes = append(nodes, Node{
				Ref:        deWinnersRef(round, number),
				Home:       winnerSlot(deWinnersRef(round-1, 2*number-1)),
				Away:       winnerSlot(deWinnersRef(round-1, 2*number)),
				Activation: ActivationUnconditional,
			})
		}
	}
	return nodes
}

func deAppendLosers(nodes []Node, size, rounds int) []Node {
	if rounds < 2 {
		// A two entrant draw. The losing finalist goes straight to the grand
		// final; see deAppendFinals.
		return nodes
	}

	// Losers round 1. The two losers of the winners round 1 matches that feed
	// the same winners round 2 match play each other. They have not met: they
	// came out of different matches.
	for number := 1; number <= size/4; number++ {
		nodes = append(nodes, Node{
			Ref:        deLosersRef(1, number),
			Home:       loserSlot(deWinnersRef(1, 2*number-1)),
			Away:       loserSlot(deWinnersRef(1, 2*number)),
			Activation: ActivationUnconditional,
		})
	}

	// drop is j in the doc comment: major round 2j receives the losers of
	// winners round j+1.
	for drop := 1; drop <= rounds-1; drop++ {
		width := size >> (drop + 1)
		for number := 1; number <= width; number++ {
			nodes = append(nodes, Node{
				Ref:        deLosersRef(2*drop, number),
				Home:       winnerSlot(deLosersRef(2*drop-1, number)),
				Away:       loserSlot(deWinnersRef(drop+1, deDropIndex(drop, number, width))),
				Activation: ActivationUnconditional,
			})
		}
		if drop > rounds-2 {
			// The last major round is the losers final; there is no minor round
			// after it.
			continue
		}
		for number := 1; number <= width/2; number++ {
			nodes = append(nodes, Node{
				Ref:        deLosersRef(2*drop+1, number),
				Home:       winnerSlot(deLosersRef(2*drop, 2*number-1)),
				Away:       winnerSlot(deLosersRef(2*drop, 2*number)),
				Activation: ActivationUnconditional,
			})
		}
	}
	return nodes
}

// deDropIndex maps a major losers round position to the winners round match
// whose loser drops into it. Reversal on odd drops, identity on even ones; the
// doc comment at the top of the file proves that this is what keeps a dropping
// player away from the player they beat in the round they just came from.
func deDropIndex(drop, number, width int) int {
	if drop%2 == 1 {
		return width + 1 - number
	}
	return number
}

func deAppendFinals(nodes []Node, rounds int) []Node {
	winnersFinal := deWinnersRef(rounds, 1)

	// With no losers bracket the beaten finalist is the losers bracket, and
	// loser_of the winners final is a distinct source_kind from winner_of, so
	// both slots can name the same match.
	fromLosers := loserSlot(winnersFinal)
	if rounds >= 2 {
		fromLosers = winnerSlot(deLosersRef(2*rounds-2, 1))
	}

	first := deFinalRef(1)
	nodes = append(nodes, Node{
		Ref:        first,
		Home:       winnerSlot(winnersFinal),
		Away:       fromLosers,
		Activation: ActivationUnconditional,
	})
	nodes = append(nodes, Node{
		Ref: deFinalRef(2),
		// Home stays the winners bracket finalist. This match only exists when
		// the away side won the first final, so the first final's loser is the
		// winners bracket finalist and its winner is the losers bracket one.
		Home:       loserSlot(first),
		Away:       winnerSlot(first),
		Activation: ActivationIfAwayWins,
		ActSrc:     first,
	})
	return nodes
}
