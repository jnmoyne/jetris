package engine

// CAS retry logic is embedded in the publish functions.
// The engine uses the rowUpdated channel to wait for consumer to deliver
// conflicting updates before re-evaluating moves.
// For simplicity in this implementation, row publishes are done individually
// with per-subject CAS. If a CAS failure occurs, the consumer will update
// the local state and the next gravity tick or move will retry.
