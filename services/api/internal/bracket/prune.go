package bracket

// Prune is the only place byes are handled, for every format.
//
// Emitters are allowed to be naive: a single elimination emitter builds a full
// power-of-two tree and marks the empty positions as byes, and a double
// elimination emitter does the same in both brackets. Prune then removes those
// matches and rewires whatever consumed them. Keeping this in one
// format-independent pass is what stops each emitter growing its own subtly
// different bye logic, which is where bracket generators usually go wrong.
//
// The rules are:
//
//   - A match with one bye and one real source is a walkover that nobody plays.
//     The match is removed and anything consuming its WINNER now consumes the
//     real source directly. Anything consuming its LOSER gets a bye, because no
//     one lost a match that was never played.
//   - A match with two byes is removed and both its winner and loser consumers
//     get a bye, which is how a bye cascades through the early rounds of a very
//     unbalanced draw.
//
// Removal can create new single-bye matches, so this runs to a fixed point.
// A finished graph contains no bye slots at all, which matters because
// match_slots has no representation for one.
//
// Match numbers are deliberately NOT renumbered afterwards. Gaps in
// match_number are harmless under UNIQUE(stage_id, bracket, round_number,
// match_number), and renumbering would mean rewriting every edge that referred
// to a moved match, which is a needless source of bugs.
func Prune(g Graph) Graph {
	nodes := make([]Node, len(g.Nodes))
	copy(nodes, g.Nodes)

	// Bounded to the node count: each pass removes at least one node, so it
	// cannot loop more times than there are nodes. The bound is a backstop
	// against a malformed graph rather than an expected limit.
	for pass := 0; pass <= len(g.Nodes); pass++ {
		victim, found := findByeMatch(nodes)
		if !found {
			break
		}
		nodes = removeAndRewire(nodes, victim)
	}

	sortNodes(nodes)
	return Graph{Format: g.Format, Nodes: nodes}
}

// findByeMatch returns the position of the first match carrying a bye, in
// canonical order so the choice is deterministic.
func findByeMatch(nodes []Node) (int, bool) {
	best := -1
	for position, node := range nodes {
		if node.Home.Kind != SourceBye && node.Away.Kind != SourceBye {
			continue
		}
		if best == -1 || node.Ref.less(nodes[best].Ref) {
			best = position
		}
	}
	return best, best != -1
}

// removeAndRewire deletes one match and repoints every edge that referenced it.
func removeAndRewire(nodes []Node, position int) []Node {
	victim := nodes[position]

	// What the winner of the removed match resolves to. With one bye it is the
	// surviving side; with two byes there is no winner and consumers inherit a
	// bye.
	winner := byeSlot()
	switch {
	case victim.Home.Kind == SourceBye && victim.Away.Kind == SourceBye:
		winner = byeSlot()
	case victim.Home.Kind == SourceBye:
		winner = victim.Away
	default:
		winner = victim.Home
	}

	remaining := make([]Node, 0, len(nodes)-1)
	for i, node := range nodes {
		if i == position {
			continue
		}
		node.Home = rewire(node.Home, victim.Ref, winner)
		node.Away = rewire(node.Away, victim.Ref, winner)
		// A conditional match whose trigger no longer exists can never fire, so
		// it becomes unconditional rather than dangling. In practice this only
		// arises for a double elimination reset in a draw small enough that the
		// grand final itself was pruned.
		if node.ActSrc == victim.Ref {
			node.Activation = ActivationUnconditional
			node.ActSrc = LocalRef{}
		}
		remaining = append(remaining, node)
	}
	return remaining
}

// rewire replaces a reference to a removed match. A winner reference becomes
// whatever that match resolved to; a loser reference becomes a bye, because an
// unplayed match produced no loser.
func rewire(slot Slot, removed LocalRef, winner Slot) Slot {
	if slot.Src != removed {
		return slot
	}
	switch slot.Kind {
	case SourceWinnerOf:
		return winner
	case SourceLoserOf:
		return byeSlot()
	}
	return slot
}
