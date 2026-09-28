package bracket

// emitSingleElimination builds a knockout tree for the ordered draw.
//
// The tree is always a full power of two. If the field is not a power of two the
// surplus positions are filled with byeSlot() and left for Prune to remove,
// which is the whole reason this emitter carries no bye logic of its own: the
// strongest entrants get their byes because seedOrder puts the high positions
// (the ones with no entry behind them) opposite the low seeds, and Prune then
// deletes the unplayable matches and advances the real entrant. Nothing here
// special-cases a field size.
//
// Positions come from seedOrder(size), which returns 1-based seed positions in
// bracket order. Round 1 pairs those positions two at a time, so match m is
// order[2m-2] against order[2m-1]. Every later round is pure structure: match m
// of round r consumes the winners of matches 2m-1 and 2m of round r-1. That
// keeps progression an edge to follow rather than arithmetic on round numbers,
// and it is what gives each match's winner exactly one consumer, as
// match_slots_one_consumer_uidx requires.
//
// A bronze match is added only when the organizer asked for one and the tree is
// deep enough to have semifinals. It sits in its own bracket and consumes the
// LOSER of each semifinal, which is the only place in this format where a loser
// edge appears. It is numbered in the same round as the final because that is
// when it is played. In a draw where a semifinal is itself a walkover the loser
// edge becomes a bye and Prune deletes the bronze match too, which is correct:
// nobody lost a match that was never played.
func emitSingleElimination(in DrawInput) ([]Node, error) {
	// Emit validates first, so this only guards a direct call. A one-entry tree
	// would have no matches at all and could not be ranked or stored.
	if len(in.Entries) < 2 {
		return nil, ErrTooFewEntries
	}

	size := nextPowerOfTwo(len(in.Entries))
	order := seedOrder(size)
	rounds := seRoundCount(size)

	// size-1 matches in the tree, plus at most one bronze.
	nodes := make([]Node, 0, size)

	for number := 1; number <= size/2; number++ {
		nodes = append(nodes, Node{
			Ref:        LocalRef{Bracket: BracketMain, Round: 1, Number: number},
			Home:       sePositionSlot(in.Entries, order[2*number-2]),
			Away:       sePositionSlot(in.Entries, order[2*number-1]),
			Activation: ActivationUnconditional,
		})
	}

	for round := 2; round <= rounds; round++ {
		for number := 1; number <= size>>round; number++ {
			nodes = append(nodes, Node{
				Ref:        LocalRef{Bracket: BracketMain, Round: round, Number: number},
				Home:       winnerSlot(LocalRef{Bracket: BracketMain, Round: round - 1, Number: 2*number - 1}),
				Away:       winnerSlot(LocalRef{Bracket: BracketMain, Round: round - 1, Number: 2 * number}),
				Activation: ActivationUnconditional,
			})
		}
	}

	if in.Config.ThirdPlace && rounds >= 2 {
		nodes = append(nodes, Node{
			Ref:        LocalRef{Bracket: BracketBronze, Round: rounds, Number: 1},
			Home:       loserSlot(LocalRef{Bracket: BracketMain, Round: rounds - 1, Number: 1}),
			Away:       loserSlot(LocalRef{Bracket: BracketMain, Round: rounds - 1, Number: 2}),
			Activation: ActivationUnconditional,
		})
	}

	sortNodes(nodes)
	return nodes, nil
}

// sePositionSlot resolves a 1-based bracket position to its occupant. Positions
// beyond the field are byes; the draw order is taken exactly as given, never
// reordered.
func sePositionSlot(entries []DrawEntry, position int) Slot {
	if position >= 1 && position <= len(entries) {
		return entrySlot(entries[position-1].EntryID)
	}
	return byeSlot()
}

// seRoundCount is log2 of a power-of-two bracket size: the number of rounds from
// the first to the final.
func seRoundCount(size int) int {
	rounds := 0
	for width := size; width > 1; width >>= 1 {
		rounds++
	}
	return rounds
}
