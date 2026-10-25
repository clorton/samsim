package simulator

// lastSelectedLane returns the lane most recently selected by
// prepareSendAttemptLocked for a brand-new (non-retry) ordinary attempt,
// for white-box test observation of fairness lane selection. Unlike
// reading e.pending.lane directly, this survives the pending packet being
// cleared by a fast (e.g. fake, synchronous) send completing before the
// test goroutine gets a chance to observe it.
func (e *Engine) lastSelectedLane() (Lane, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.testLastSelectedLaneSet {
		return "", false
	}
	return e.testLastSelectedLane, true
}

// selectionCount returns the number of ordinary (non-retry) lane selections
// made so far. It is incremented synchronously within Tick's locked
// section, so (unlike a test fake sender's sentCount, which is updated
// later from a separate dispatch goroutine) it can be read reliably
// immediately after Tick returns.
func (e *Engine) selectionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.testSelectionCount
}
