package coordinator

// maxWithdrawn bounds the nodes whose withdrawal is remembered, and
// maxErrandDevices those the errand board knows of.
const (
	maxWithdrawn     = 1 << 16
	maxErrandDevices = 1 << 16
)

// maxCarrierCandidates is how many of the devices that could carry a
// question are looked up: each costs two queries under the engine's lock.
const maxCarrierCandidates = 32

// Authorize reports whether peer may use the control plane at all: a device
// of an active user, an audit identity, or any peer where accounts are open.
// It reads outside the engine's lock, for requests that need nothing else
// from the database (the model list, an errand poll, a withdrawal).
func (e *Engine) Authorize(peer string) error {
	_, err := e.accountOf(e.db, peer)
	return err
}
