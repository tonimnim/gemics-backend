package bracket

// Rank assigns each match its longest-path layer: a match fed only by direct
// entries is rank 1, and any other match is one more than the deepest match it
// depends on.
//
// This exists because match_slots enforces CHECK (source_rank < match_rank).
// That constraint is what makes acyclicity a property of the schema rather than
// a promise from the generator, so a future bug that wired an edge backwards
// would be refused by PostgreSQL rather than silently creating a tournament
// that can never finish.
//
// Longest path rather than shortest is deliberate. In double elimination a
// losers-bracket match is fed by both a winners-bracket match and an earlier
// losers-bracket match, and only the deeper of the two guarantees the strict
// inequality holds for every edge.
func Rank(g Graph) Graph {
	nodes := make([]Node, len(g.Nodes))
	copy(nodes, g.Nodes)
	sortNodes(nodes)

	positions := index(nodes)
	ranks := make([]int, len(nodes))
	// 0 = unvisited, 1 = in progress, 2 = done. In progress detects a cycle,
	// which would otherwise recurse until the stack gave out.
	state := make([]int, len(nodes))

	var resolve func(position int) int
	resolve = func(position int) int {
		switch state[position] {
		case 2:
			return ranks[position]
		case 1:
			// A cycle cannot be ranked. Returning the largest possible depth
			// forces the SQL constraint to reject the graph at insert time
			// rather than letting it look plausible.
			return len(nodes) + 1
		}
		state[position] = 1
		depth := 0
		for _, slot := range []Slot{nodes[position].Home, nodes[position].Away} {
			if slot.Kind != SourceWinnerOf && slot.Kind != SourceLoserOf {
				continue
			}
			source, ok := positions[slot.Src]
			if !ok {
				// A dangling edge is a generator bug. Skipping it here keeps
				// ranking total; validate() in the persistence layer is what
				// refuses to store it.
				continue
			}
			if sourceRank := resolve(source); sourceRank > depth {
				depth = sourceRank
			}
		}
		ranks[position] = depth + 1
		state[position] = 2
		return ranks[position]
	}

	for position := range nodes {
		resolve(position)
	}
	for position := range nodes {
		nodes[position].Rank = ranks[position]
	}
	return Graph{Format: g.Format, Nodes: nodes, Fingerprint: g.Fingerprint}
}

// Validate checks the invariants a graph must satisfy before it can be stored.
// It is the Go-side mirror of the match_slots constraints, run first so a bug
// surfaces as a clear error rather than a constraint violation from the driver.
func Validate(g Graph) error {
	positions := index(g.Nodes)
	seenConsumer := make(map[string]LocalRef, len(g.Nodes)*2)

	for _, node := range g.Nodes {
		if node.Rank < 1 {
			return &GraphError{Ref: node.Ref, Reason: "rank was not assigned"}
		}
		for side, slot := range map[string]Slot{"home": node.Home, "away": node.Away} {
			switch slot.Kind {
			case SourceEntry:
				if slot.EntryID == "" {
					return &GraphError{Ref: node.Ref, Reason: side + " entry slot has no entry id"}
				}
			case SourceWinnerOf, SourceLoserOf:
				source, ok := positions[slot.Src]
				if !ok {
					return &GraphError{Ref: node.Ref, Reason: side + " slot points at " + slot.Src.String() + " which does not exist"}
				}
				if g.Nodes[source].Rank >= node.Rank {
					return &GraphError{Ref: node.Ref, Reason: side + " slot points at " + slot.Src.String() + " which is not an earlier layer"}
				}
				// match_slots_one_consumer_uidx: a match's winner feeds at most
				// one slot, and so does its loser.
				key := slot.Src.String() + "/" + slot.Kind.String()
				if previous, taken := seenConsumer[key]; taken {
					return &GraphError{Ref: node.Ref, Reason: "the " + slot.Kind.String() + " of " + slot.Src.String() +
						" is already consumed by " + previous.String()}
				}
				seenConsumer[key] = node.Ref
			case SourceBye:
				return &GraphError{Ref: node.Ref, Reason: side + " slot is still a bye after pruning"}
			default:
				return &GraphError{Ref: node.Ref, Reason: side + " slot has no source"}
			}
		}
		if node.Activation != ActivationUnconditional && node.ActSrc.zero() {
			return &GraphError{Ref: node.Ref, Reason: "conditional match names no activation source"}
		}
		if node.Activation == ActivationUnconditional && !node.ActSrc.zero() {
			return &GraphError{Ref: node.Ref, Reason: "unconditional match carries an activation source"}
		}
		if !node.ActSrc.zero() {
			if _, ok := positions[node.ActSrc]; !ok {
				return &GraphError{Ref: node.Ref, Reason: "activation source " + node.ActSrc.String() + " does not exist"}
			}
		}
	}
	return nil
}

// GraphError names the match that failed validation, because "invalid graph" on
// a 500 match double elimination draw is not a debuggable message.
type GraphError struct {
	Ref    LocalRef
	Reason string
}

func (e *GraphError) Error() string { return "bracket: " + e.Ref.String() + ": " + e.Reason }
