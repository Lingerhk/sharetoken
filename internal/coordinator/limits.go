package coordinator

import "sharetoken/internal/protocol"

// Limits on what one account can take from everyone else: the coordinator's
// memory (offers), nodes' slots (tickets that never start) and the platform's
// fees (audit jobs). Each default is far above what a client does.

// ctrlFrameLimit is the size a control request of type typ may have. An
// offer is bounded by protocol's offer limits, and the requests that carry
// nothing or a timestamp are small; the rest (tickets with their offers'
// addresses, receipts, evidence headers) keep the general limit. A frame
// that does not begin with its type, as every client writes it, is "".
func ctrlFrameLimit(typ string) int {
	switch typ {
	case protocol.TypeAnnounce:
		return protocol.MaxOfferFrame
	case protocol.TypeWithdraw, protocol.TypeModels, protocol.TypeBalance, protocol.TypeErrandPoll, "":
		return protocol.MaxSmallFrame
	}
	return protocol.MaxFrameSize
}

// offersOf counts the live offers of acct's nodes other than node. The
// caller holds e.mu.
func (e *Engine) offersOf(acct, node string, now int64) int {
	n := 0
	for id, oe := range e.offers {
		if id != node && oe.acct == acct && oe.offer.ExpiresAt > now {
			n++
		}
	}
	return n
}
