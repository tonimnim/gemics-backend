// Package bracket generates and advances tournament graphs.
//
// The generator is pure. Given the same ordered entries, seed and config it
// produces byte-identical output, because progression must be replayable and a
// draw must be defensible months later when a player asks why they were placed
// where they were. Nothing here reads a clock, a random source, or iterates a
// map in a way that could vary between runs; graph_test.go asserts that.
//
// The central idea is that a match records where its participants come from
// rather than leaving it to be inferred. A slot is either a direct entry or the
// winner or loser of an earlier match. Progression is then edge-following, never
// arithmetic on round numbers, which is what docs/architecture.md requires.
package bracket

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Format string

const (
	SingleElimination Format = "single_elimination"
	DoubleElimination Format = "double_elimination"
	RoundRobin        Format = "round_robin"
)

// Bracket names. matches.bracket has no CHECK constraint, so these are
// conventions enforced here rather than in SQL.
const (
	BracketMain        = "main"
	BracketWinners     = "winners"
	BracketLosers      = "losers"
	BracketGrandFinal  = "grand_final"
	BracketBronze      = "bronze"
	GroupBracketPrefix = "group_"
)

// Activation rules. A conditional match exists in the graph from draw time but
// only becomes playable if its source match resolves a particular way. The one
// real use is the double elimination bracket reset.
const (
	ActivationUnconditional = "unconditional"
	ActivationIfAwayWins    = "if_source_away_wins"
)

type SourceKind uint8

const (
	// SourceEmpty is the zero value and is never valid in a finished graph.
	SourceEmpty SourceKind = iota
	SourceEntry
	SourceWinnerOf
	SourceLoserOf
	// SourceBye exists only in memory, between Emit and Prune. A bye is the
	// absence of an opponent, so it must never reach the database: Prune
	// removes it by advancing the real entrant directly.
	SourceBye
)

func (kind SourceKind) String() string {
	switch kind {
	case SourceEntry:
		return "entry"
	case SourceWinnerOf:
		return "winner_of"
	case SourceLoserOf:
		return "loser_of"
	case SourceBye:
		return "bye"
	}
	return "empty"
}

// LocalRef identifies a match inside one generated graph. It is deliberately
// not a database id: the generator runs before anything is persisted, and tests
// can then assert on a readable coordinate rather than a UUID.
type LocalRef struct {
	Bracket string
	Round   int
	Number  int
}

func (ref LocalRef) String() string {
	return ref.Bracket + "-R" + strconv.Itoa(ref.Round) + "-M" + strconv.Itoa(ref.Number)
}

func (ref LocalRef) zero() bool { return ref.Bracket == "" && ref.Round == 0 && ref.Number == 0 }

// less gives a total order over refs so a graph can be sorted canonically.
func (ref LocalRef) less(other LocalRef) bool {
	if ref.Bracket != other.Bracket {
		return ref.Bracket < other.Bracket
	}
	if ref.Round != other.Round {
		return ref.Round != other.Round && ref.Round < other.Round
	}
	return ref.Number < other.Number
}

// Slot is one side of a match and answers "who plays here".
type Slot struct {
	Kind SourceKind
	// EntryID is set only when Kind is SourceEntry.
	EntryID string
	// Src is set only when Kind is SourceWinnerOf or SourceLoserOf.
	Src LocalRef
}

func entrySlot(entryID string) Slot { return Slot{Kind: SourceEntry, EntryID: entryID} }
func winnerSlot(src LocalRef) Slot  { return Slot{Kind: SourceWinnerOf, Src: src} }
func loserSlot(src LocalRef) Slot   { return Slot{Kind: SourceLoserOf, Src: src} }
func byeSlot() Slot                 { return Slot{Kind: SourceBye} }

// Node is a generated match.
type Node struct {
	Ref        LocalRef
	Home, Away Slot
	// Rank is the longest-path layer, assigned by Rank. Every edge must point
	// at a strictly lower rank, which is what makes the graph acyclic and is
	// enforced again by match_slots_acyclic_chk in SQL.
	Rank int
	// GroupKey is set for round-robin groups and is nil-equivalent otherwise.
	GroupKey string
	// Activation and ActSrc express a conditional match such as the double
	// elimination bracket reset.
	Activation string
	ActSrc     LocalRef
}

// Graph is the full generated tournament.
type Graph struct {
	Format      Format
	Nodes       []Node
	Fingerprint [32]byte
}

// DrawEntry is one competitor in the canonical ordered input vector. The order
// is the draw: callers decide it (by seed, rating, or a seeded shuffle) and it
// is stored so the graph can be regenerated and compared.
type DrawEntry struct {
	EntryID  string
	SeedKey  int
	GroupKey string
}

// Config carries the format options an organizer chooses.
type Config struct {
	// BestOf is carried through to the stage, not used by the generator.
	BestOf int
	// ThirdPlace adds a bronze match between the two losing semifinalists.
	// Single elimination only.
	ThirdPlace bool
	// GroupCount splits a round robin into that many groups. Zero or one means
	// a single table.
	GroupCount int
	// DoubleRoundRobin plays every pairing twice, home and away reversed.
	DoubleRoundRobin bool
}

// DrawInput is everything the pure generator sees.
type DrawInput struct {
	Format  Format
	Entries []DrawEntry
	Config  Config
}

var (
	ErrTooFewEntries  = errors.New("bracket: a competition needs at least two entries")
	ErrTooManyEntries = errors.New("bracket: entry count exceeds the supported maximum")
	ErrUnknownFormat  = errors.New("bracket: unknown format")
	ErrDuplicateEntry = errors.New("bracket: duplicate entry in the draw")
	ErrEmptyEntryID   = errors.New("bracket: an entry has no id")
)

// MaxEntries mirrors the organizer capacity ceiling. A double elimination draw
// at this size is a little over two thousand matches, which is comfortably
// inside the bracket read path's limits.
const MaxEntries = 1024

// Emit builds the graph for a format. It validates the input, dispatches to the
// format generator, removes byes and assigns ranks, so callers get a graph that
// is already safe to persist.
func Emit(in DrawInput) (Graph, error) {
	if err := validate(in); err != nil {
		return Graph{}, err
	}
	var nodes []Node
	var err error
	switch in.Format {
	case SingleElimination:
		nodes, err = emitSingleElimination(in)
	case DoubleElimination:
		nodes, err = emitDoubleElimination(in)
	case RoundRobin:
		nodes, err = emitRoundRobin(in)
	default:
		return Graph{}, fmt.Errorf("%w: %q", ErrUnknownFormat, in.Format)
	}
	if err != nil {
		return Graph{}, err
	}
	graph := Graph{Format: in.Format, Nodes: nodes}
	graph = Prune(graph)
	graph = Rank(graph)
	graph.Fingerprint = Fingerprint(graph)
	return graph, nil
}

func validate(in DrawInput) error {
	if len(in.Entries) < 2 {
		return ErrTooFewEntries
	}
	if len(in.Entries) > MaxEntries {
		return fmt.Errorf("%w: %d entries, maximum %d", ErrTooManyEntries, len(in.Entries), MaxEntries)
	}
	seen := make(map[string]struct{}, len(in.Entries))
	for _, entry := range in.Entries {
		if strings.TrimSpace(entry.EntryID) == "" {
			return ErrEmptyEntryID
		}
		if _, duplicate := seen[entry.EntryID]; duplicate {
			return fmt.Errorf("%w: %s", ErrDuplicateEntry, entry.EntryID)
		}
		seen[entry.EntryID] = struct{}{}
	}
	return nil
}

// sortNodes puts a graph in canonical order so the fingerprint is stable and
// tests can rely on positions. Ordering is by bracket, then round, then number.
func sortNodes(nodes []Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		left, right := nodes[i].Ref, nodes[j].Ref
		if left.Bracket != right.Bracket {
			return left.Bracket < right.Bracket
		}
		if left.Round != right.Round {
			return left.Round < right.Round
		}
		return left.Number < right.Number
	})
}

// Fingerprint is a stable digest of the graph's shape and its entry placement.
// Two draws with the same fingerprint are the same tournament. It deliberately
// excludes Rank, which is derived, so a change in the ranking algorithm alone
// does not invalidate stored draws.
func Fingerprint(g Graph) [32]byte {
	nodes := make([]Node, len(g.Nodes))
	copy(nodes, g.Nodes)
	sortNodes(nodes)

	var builder strings.Builder
	builder.WriteString(string(g.Format))
	builder.WriteByte('\n')
	for _, node := range nodes {
		builder.WriteString(node.Ref.String())
		builder.WriteByte('|')
		builder.WriteString(node.GroupKey)
		builder.WriteByte('|')
		builder.WriteString(node.Activation)
		builder.WriteByte('|')
		if !node.ActSrc.zero() {
			builder.WriteString(node.ActSrc.String())
		}
		builder.WriteByte('|')
		writeSlot(&builder, node.Home)
		builder.WriteByte('|')
		writeSlot(&builder, node.Away)
		builder.WriteByte('\n')
	}
	return sha256.Sum256([]byte(builder.String()))
}

func writeSlot(builder *strings.Builder, slot Slot) {
	builder.WriteString(slot.Kind.String())
	builder.WriteByte(':')
	switch slot.Kind {
	case SourceEntry:
		builder.WriteString(slot.EntryID)
	case SourceWinnerOf, SourceLoserOf:
		builder.WriteString(slot.Src.String())
	}
}

// index builds a lookup from ref to position. Callers must not hold it across a
// mutation of the slice.
func index(nodes []Node) map[LocalRef]int {
	positions := make(map[LocalRef]int, len(nodes))
	for position, node := range nodes {
		positions[node.Ref] = position
	}
	return positions
}

// nextPowerOfTwo returns the smallest power of two greater than or equal to n.
func nextPowerOfTwo(n int) int {
	size := 1
	for size < n {
		size <<= 1
	}
	return size
}

// seedOrder returns the standard single elimination seeding order for a bracket
// of the given power-of-two size, as 1-based seed positions.
//
// It is built by folding: a bracket of size 2n is the bracket of size n with
// each position x paired against 2n+1-x. That is what puts seed 1 against the
// lowest seed, keeps 1 and 2 apart until the final, and gives byes to the
// strongest entrants when the field is not a power of two.
func seedOrder(size int) []int {
	order := []int{1}
	for len(order) < size {
		width := len(order) * 2
		next := make([]int, 0, width)
		for _, position := range order {
			next = append(next, position, width+1-position)
		}
		order = next
	}
	return order
}
