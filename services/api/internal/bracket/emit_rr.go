package bracket

// Round robin is the one format with no dependency edges at all. Every slot is a
// direct entry, so every match is rank 1 and the whole fixture list is playable
// the moment the draw is made. That is exactly what match_slots calls out for
// this format: only 'entry' rows, never winner_of or loser_of, and therefore
// nothing for match_slots_one_consumer_uidx or the acyclicity check to catch.
//
// The schedule is the circle method, the construction behind the Berger tables
// used for chess and most league scheduling. Entrants are laid out around a
// circle, one position is pinned and the other size-1 positions rotate a step
// each round. Every rotation is a different perfect matching of the circle, and
// after size-1 rotations every pair has met exactly once. That is the whole
// correctness argument: it is a property of the rotation, not of any arithmetic
// on round numbers.
//
// An odd field is scheduled as if it had one more entrant. The extra position is
// a phantom: whoever is drawn against it has no opponent that round, which this
// package represents as a bye slot and leaves for Prune to remove, the same as
// for every other format. This emitter never reasons about byes itself.
//
// The pinned position holds the first entrant when the field is even and the
// phantom when it is odd. That second case is deliberate. An entrant who sits a
// round out loses one fixture from their home/away sequence, and if the pinned
// position could be the one to lose it the skew would never wash out: a three
// entrant table ends up with one entrant at home twice and another away twice.
// Pinning the phantom instead rotates every real entrant and makes the split for
// an odd field come out exactly even.
//
// Nothing here reorders the input. in.Entries is the draw, decided upstream, and
// the only thing position in that vector affects is which slice of the circle an
// entrant occupies and, with groups, which group deals them.

// emitRoundRobin builds the fixture list for a round robin, optionally split
// into groups and optionally played twice.
func emitRoundRobin(in DrawInput) ([]Node, error) {
	// Emit validates first, so this only fires when the emitter is called
	// directly. It is still worth having: a one-entrant round robin is an empty
	// fixture list, and returning that silently would look like a working draw.
	if len(in.Entries) < 2 {
		return nil, ErrTooFewEntries
	}

	groups := roundRobinGroups(in.Config.GroupCount, len(in.Entries))
	nodes := make([]Node, 0, roundRobinCapacity(len(in.Entries), groups, in.Config.DoubleRoundRobin))

	for group := 0; group < groups; group++ {
		// A single table is the main bracket with no group key, because there is
		// no group to name. Groups become their own bracket, which is what lets
		// UNIQUE(stage_id, bracket, round_number, match_number) carry eight
		// parallel tables with no schema change.
		bracketName, groupKey := BracketMain, ""
		if groups > 1 {
			bracketName = GroupBracketPrefix + groupLabel(group)
			groupKey = bracketName
		}
		nodes = append(nodes, circleSchedule(
			dealGroup(in.Entries, groups, group),
			bracketName,
			groupKey,
			in.Config.DoubleRoundRobin,
		)...)
	}

	sortNodes(nodes)
	return nodes, nil
}

// circleSchedule is the circle method for one table.
//
// Home and away are assigned by column: the entrant on the near side of the
// circle is home. Column zero is the exception, because it always holds the
// pinned position, and a pinned entrant assigned the same side every round would
// play every fixture at home. Alternating that one column by round parity fixes
// it, and because the rotation carries a different opponent through column zero
// each round the correction is spread over the whole field. Every entrant then
// ends within one fixture of an even split, and exactly level whenever the
// number of fixtures they play is even.
//
// For an odd field column zero is the phantom's fixture, so the alternation
// there is moot and Prune deletes the match: match number 1 is absent from every
// round, which is the gap Prune's contract says not to renumber away.
func circleSchedule(members []DrawEntry, bracketName, groupKey string, double bool) []Node {
	// One entrant has nobody to play. This is only reachable through a group
	// count the field cannot fill, which roundRobinGroups already caps, so
	// treating it as an empty schedule is a backstop rather than a case.
	if len(members) < 2 {
		return nil
	}

	// An odd field is padded to the next even size and the padding sits at the
	// pinned position, so entrants occupy positions offset..size-1 and position
	// zero is the phantom. offset is 1 exactly when a phantom exists.
	size := len(members) + len(members)%2
	offset := size - len(members)
	rounds := size - 1
	perRound := size / 2

	legs := 1
	if double {
		legs = 2
	}

	nodes := make([]Node, 0, rounds*perRound*legs)
	positions := make([]int, size)

	for leg := 0; leg < legs; leg++ {
		for round := 0; round < rounds; round++ {
			rotateCircle(positions, round)
			for column := 0; column < perRound; column++ {
				home, away := positions[column], positions[size-1-column]
				if column == 0 && round%2 == 1 {
					home, away = away, home
				}
				// The second leg is the first leg played back to front, so a
				// double round robin gives every entrant exactly as many home
				// fixtures as away ones.
				if leg == 1 {
					home, away = away, home
				}
				nodes = append(nodes, Node{
					Ref: LocalRef{
						Bracket: bracketName,
						// Round numbering continues through the second leg
						// rather than restarting, so round release and the
						// UNIQUE on (bracket, round_number, match_number) both
						// see one ordered season.
						Round:  leg*rounds + round + 1,
						Number: column + 1,
					},
					Home:       circleSlot(members, offset, home),
					Away:       circleSlot(members, offset, away),
					GroupKey:   groupKey,
					Activation: ActivationUnconditional,
				})
			}
		}
	}
	return nodes
}

// rotateCircle writes the circle layout for one round into positions. Index 0 is
// pinned; the rest advance one step per round around a cycle of size-1. The
// pairing for the round is then position i against position size-1-i.
func rotateCircle(positions []int, round int) {
	cycle := len(positions) - 1
	positions[0] = 0
	for place := 1; place < len(positions); place++ {
		positions[place] = (place-1+round)%cycle + 1
	}
}

// circleSlot turns a circle position into a slot. Positions below the offset are
// the phantom padding an odd field, and a fixture against the phantom is a bye.
func circleSlot(members []DrawEntry, offset, position int) Slot {
	member := position - offset
	if member < 0 || member >= len(members) {
		return byeSlot()
	}
	return entrySlot(members[member].EntryID)
}

// dealGroup takes every groups-th entry starting at group, which deals the draw
// out like cards. Consecutive entries land in different groups, so a field
// ordered by strength is spread across the groups instead of stacking the top
// seeds into group A. Relative order inside a group is preserved.
func dealGroup(entries []DrawEntry, groups, group int) []DrawEntry {
	members := make([]DrawEntry, 0, len(entries)/groups+1)
	for position := group; position < len(entries); position += groups {
		members = append(members, entries[position])
	}
	return members
}

// roundRobinGroups resolves the configured group count against the field.
//
// A group needs two entrants to produce a fixture at all, so a count the field
// cannot fill is capped rather than honoured literally. Honouring it would hand
// somebody an empty schedule, and for a small field it could produce a draw with
// no matches whatsoever, which competition_draws.match_count > 0 would refuse
// anyway.
func roundRobinGroups(configured, entries int) int {
	if configured < 2 {
		return 1
	}
	if fillable := entries / 2; configured > fillable {
		return fillable
	}
	return configured
}

// groupLabel names a group the way a spreadsheet names a column: a..z, then
// aa..az. Only the first handful are ever seen, but the continuation means a
// large group count cannot produce two groups sharing a bracket name.
func groupLabel(group int) string {
	var label []byte
	for {
		label = append([]byte{byte('a' + group%26)}, label...)
		group = group/26 - 1
		if group < 0 {
			return string(label)
		}
	}
}

// roundRobinCapacity is the exact pre-prune node count, used only to size the
// slice. A full round robin is quadratic in the field, so growing by append
// would reallocate a lot for a large draw.
func roundRobinCapacity(entries, groups int, double bool) int {
	total := 0
	for group := 0; group < groups; group++ {
		members := entries / groups
		if group < entries%groups {
			members++
		}
		if members < 2 {
			continue
		}
		size := members + members%2
		total += (size - 1) * (size / 2)
	}
	if double {
		total *= 2
	}
	return total
}
