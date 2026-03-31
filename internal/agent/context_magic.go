package agent

// ContextMagic holds the raw conversation history and uses a pluggable
// ContextStrategy to decide which messages are provided to the LLM.
type ContextMagic struct {
	history  []Message
	strategy ContextStrategy
}

// NewContextMagic creates a ContextMagic with the given strategy.
// If strategy is nil, FullHistoryStrategy is used.
func NewContextMagic(strategy ContextStrategy) *ContextMagic {
	if strategy == nil {
		strategy = &FullHistoryStrategy{}
	}
	return &ContextMagic{strategy: strategy}
}

// Append adds a message to the raw history.
func (c *ContextMagic) Append(msg Message) {
	c.history = append(c.history, msg)
}

// Messages returns the strategy-filtered messages for the LLM request.
func (c *ContextMagic) Messages() []Message {
	return c.strategy.Select(c.history)
}

// RawHistory returns the full unfiltered conversation history.
func (c *ContextMagic) RawHistory() []Message {
	return c.history
}

// Len returns the length of the raw history.
func (c *ContextMagic) Len() int {
	return len(c.history)
}

// SetHistory replaces the raw history (used by compaction).
func (c *ContextMagic) SetHistory(msgs []Message) {
	c.history = msgs
}

// SetStrategy swaps the active strategy at runtime.
func (c *ContextMagic) SetStrategy(s ContextStrategy) {
	c.strategy = s
}
