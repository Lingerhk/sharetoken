// Package protocol defines sharetoken's wire messages, their canonical signing
// encodings, and the framing used on libp2p streams.
//
// Trust anchors: every party is a libp2p Ed25519 identity. Offers are signed by
// the node, tickets by the coordinator, receipts by the node's receipt key,
// acks by the buyer — and each signer's public key is recoverable from its peer
// ID (see PubKeyFromPeerID).
//
// Control plane (user <-> coordinator, always through a relay): CtrlProtocol,
// one request and one response per stream. Data plane (buyer <-> node, direct
// or via relay): InferProtocol. The coordinator is never on the data plane.
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"slices"
	"time"

	"sharetoken/internal/attest"
	"sharetoken/internal/box"
	"sharetoken/internal/canon"
	"sharetoken/internal/receipt"
	"sharetoken/internal/version"
)

// libp2p protocol IDs.
const (
	InferProtocol = "/sharetoken/infer/1.0.2"
	CtrlProtocol  = "/sharetoken/ctrl/1.0.0"
)

// InferVersion is the buyer <-> node protocol this build prefers. It is agreed
// per job: the node's offer and the buyer's request list what each speaks, and
// the ticket names one that both do.
const InferVersion = 1

// SupportedInferVersions lists the infer protocol versions this build speaks.
var SupportedInferVersions = []uint32{InferVersion}

// BestInferVersion returns the highest version in both lists, or 0.
func BestInferVersion(a, b []uint32) uint32 {
	var best uint32
	for _, v := range a {
		if v > best && slices.Contains(b, v) {
			best = v
		}
	}
	return best
}

// Versions double as signing domains.
const (
	OfferVersion  = "sharetoken/offer/v1"
	TicketVersion = "sharetoken/ticket/v1"
	ackDomain     = "sharetoken/ack/v1"
	maxClockSkew  = time.Minute
)

// Frame types.
const (
	TypeError         = "error"
	TypeAnnounce      = "announce" // a node's signed offer, sent to the coordinator
	TypeWithdraw      = "withdraw" // a node stopped sharing; its offer ends now
	TypeAbandon       = "abandon"  // a node gives up a ticketed job it could not deliver
	TypeStarted       = "started"  // a node reports that the buyer started a ticketed job
	TypeTicketRequest = "ticket_request"
	TypeTicket        = "ticket"
	TypeSettle        = "settle"
	TypeSettled       = "settled"
	TypeModels        = "models"
	TypeBalance       = "balance"
	TypeDispute       = "dispute"
	TypeOK            = "ok"
	TypeInfer         = "infer"
	TypeCancel        = "cancel" // buyer -> node: stop the job, the user does not want the rest
	TypeResume        = "resume" // buyer -> node: continue a job whose stream broke
	TypeReceipt       = "receipt"
	TypeAck           = "ack"
	TypeReject        = "reject"
	TypeEvidence      = "evidence"
	TypeVerdict       = "verdict"
)

// ModelPrice is a node's price for one model. Amounts are micro-credits
// (1e-6 credit) per one million tokens; all money math is integer.
type ModelPrice struct {
	Model          string `json:"model"`
	PriceInMicros  uint64 `json:"price_in"`
	PriceOutMicros uint64 `json:"price_out"`
}

// Offer is a node's signed advertisement, announced to the coordinator
// (TypeAnnounce) every few seconds; it doubles as the node's heartbeat.
type Offer struct {
	Version        string        `json:"v"`
	Node           string        `json:"node"`        // libp2p peer ID; signs the offer
	Addrs          []string      `json:"addrs"`       // dialable multiaddrs, may include /p2p-circuit
	BoxPub         []byte        `json:"box_pub"`     // X25519 key requests are sealed to
	ReceiptKey     []byte        `json:"receipt_key"` // Ed25519 key receipts are signed with
	Models         []ModelPrice  `json:"models"`
	MaxConcurrency uint32        `json:"max_concurrency"`
	IssuedAt       int64         `json:"issued_at"` // unix nanos
	ExpiresAt      int64         `json:"expires_at"`
	Quote          *attest.Quote `json:"quote,omitempty"` // optional TEE quote binding ReceiptKey and BoxPub
	ClientVersion  string        `json:"client_version"`  // the node's release
	InferVersions  []uint32      `json:"infer_versions"`  // infer protocol versions the node speaks
	Sig            []byte        `json:"sig"`
}

func (o *Offer) signingBytes() []byte {
	e := canon.New(OfferVersion).
		String(o.Node).
		Strings(o.Addrs).
		Bytes(o.BoxPub).
		Bytes(o.ReceiptKey).
		U64(uint64(len(o.Models)))
	for _, m := range o.Models {
		e.String(m.Model).U64(m.PriceInMicros).U64(m.PriceOutMicros)
	}
	e.U64(uint64(o.MaxConcurrency)).I64(o.IssuedAt).I64(o.ExpiresAt).Bool(o.Quote != nil)
	if q := o.Quote; q != nil {
		e.String(q.Backend).Bytes(q.Evidence)
	}
	e.String(o.ClientVersion).U64(uint64(len(o.InferVersions)))
	for _, v := range o.InferVersions {
		e.U64(uint64(v))
	}
	return e.Sum()
}

// Sign signs the offer with the node's libp2p identity key. Unless already set,
// the offer says what this build is and speaks.
func (o *Offer) Sign(priv ed25519.PrivateKey) {
	o.Version = OfferVersion
	if o.ClientVersion == "" {
		o.ClientVersion = version.Version
	}
	if len(o.InferVersions) == 0 {
		o.InferVersions = SupportedInferVersions
	}
	o.Sig = ed25519.Sign(priv, o.signingBytes())
}

// VerifySignature checks the offer is well formed and signed by o.Node. It does
// not check the validity window (see Verify) or the hardware quote: that needs
// vendor verifiers and a measurement allow-list (attest.Verify), which is the
// verifying party's policy.
func (o *Offer) VerifySignature() error {
	if o.Version != OfferVersion {
		return errors.New("offer: unknown version")
	}
	if len(o.BoxPub) != box.PublicKeySize {
		return errors.New("offer: bad box key")
	}
	if len(o.ReceiptKey) != ed25519.PublicKeySize {
		return errors.New("offer: bad receipt key")
	}
	if len(o.Models) == 0 {
		return errors.New("offer: no models")
	}
	if o.ClientVersion == "" || len(o.InferVersions) == 0 {
		return errors.New("offer: no client or protocol version")
	}
	seen := make(map[string]bool, len(o.Models))
	for _, m := range o.Models {
		if m.Model == "" || seen[m.Model] {
			return errors.New("offer: empty or duplicate model id")
		}
		seen[m.Model] = true
	}
	if o.Quote != nil && (o.Quote.Backend == "" || len(o.Quote.Evidence) == 0) {
		return errors.New("offer: empty attestation quote")
	}
	return verifySig(o.Node, o.signingBytes(), o.Sig)
}

// Verify checks the signature and that the offer is currently valid.
func (o *Offer) Verify(now time.Time, maxTTL time.Duration) error {
	if err := o.VerifySignature(); err != nil {
		return err
	}
	n := now.UnixNano()
	switch {
	case o.ExpiresAt <= n:
		return errors.New("offer: expired")
	case o.ExpiresAt-o.IssuedAt > int64(maxTTL):
		return errors.New("offer: validity window too long")
	case o.IssuedAt > n+int64(maxClockSkew):
		return errors.New("offer: issued in the future")
	}
	return nil
}

// Price returns the node's price for model.
func (o *Offer) Price(model string) (ModelPrice, bool) {
	for _, m := range o.Models {
		if m.Model == model {
			return m, true
		}
	}
	return ModelPrice{}, false
}

// Ticket is the coordinator's signed authorization for one job: which buyer may
// ask which node to run which model, at what price, up to what reservation.
// It copies the node's keys and addresses from the node-signed offer so the
// node can check it is the intended server.
type Ticket struct {
	Version        string   `json:"v"`
	JobID          string   `json:"job_id"`
	Model          string   `json:"model"`
	Node           string   `json:"node"`
	NodeAddrs      []string `json:"node_addrs"`
	NodeBoxPub     []byte   `json:"node_box_pub"`
	NodeReceiptKey []byte   `json:"node_receipt_key"`
	Buyer          string   `json:"buyer"`
	PriceInMicros  uint64   `json:"price_in"`
	PriceOutMicros uint64   `json:"price_out"`
	ReservedMicros uint64   `json:"reserved"`   // held from the buyer; settlement never exceeds it
	IssuedAt       int64    `json:"issued_at"`  // unix nanos
	ExpiresAt      int64    `json:"expires_at"` // the node must accept the job before this
	Coordinator    string   `json:"coordinator"`
	InferVersion   uint32   `json:"infer_version"` // the infer protocol version buyer and node use for this job
	// Shared marks a job between share partners (accounts/sharing.go): the
	// node runs it as any other, but it costs nothing: no credits are held,
	// charged or paid. The prices still bound the output by the reservation.
	Shared bool   `json:"shared,omitempty"`
	Sig    []byte `json:"sig"`
}

func (t *Ticket) signingBytes() []byte {
	return canon.New(TicketVersion).
		String(t.JobID).
		String(t.Model).
		String(t.Node).
		Strings(t.NodeAddrs).
		Bytes(t.NodeBoxPub).
		Bytes(t.NodeReceiptKey).
		String(t.Buyer).
		U64(t.PriceInMicros).
		U64(t.PriceOutMicros).
		U64(t.ReservedMicros).
		I64(t.IssuedAt).
		I64(t.ExpiresAt).
		String(t.Coordinator).
		U64(uint64(t.InferVersion)).
		Bool(t.Shared).
		Sum()
}

// Sign signs the ticket with the coordinator's identity key.
func (t *Ticket) Sign(priv ed25519.PrivateKey) {
	t.Version = TicketVersion
	t.Sig = ed25519.Sign(priv, t.signingBytes())
}

// Verify checks the ticket was signed by the expected coordinator.
func (t *Ticket) Verify(coordinator string) error {
	if t.Version != TicketVersion {
		return errors.New("ticket: unknown version")
	}
	if t.Coordinator != coordinator {
		return errors.New("ticket: not issued by the trusted coordinator")
	}
	return verifySig(t.Coordinator, t.signingBytes(), t.Sig)
}

// Equal reports whether two tickets are byte-identical, signature included.
func (t *Ticket) Equal(o *Ticket) bool {
	return bytes.Equal(t.signingBytes(), o.signingBytes()) && bytes.Equal(t.Sig, o.Sig)
}

// RequestAAD binds a sealed request to one job, buyer and node, so it cannot be
// replayed into another job or to another node.
func RequestAAD(t *Ticket) []byte {
	return canon.New("sharetoken/aad/request/v1").
		String(t.JobID).String(t.Model).String(t.Buyer).String(t.Node).Sum()
}

// ChunkAAD binds a sealed response chunk to its job and position, so chunks
// cannot be dropped, reordered or spliced in from another job.
func ChunkAAD(t *Ticket, seq uint64) []byte {
	return canon.New("sharetoken/aad/chunk/v1").String(t.JobID).String(t.Model).U64(seq).Sum()
}

// AckBytes is what a buyer signs to acknowledge a receipt: "I checked this
// transcript and accept these usage numbers."
func AckBytes(r *receipt.Receipt) []byte {
	return canon.New(ackDomain).Bytes(r.Digest()).Sum()
}

// TicketRequest asks the coordinator to match a node.
type TicketRequest struct {
	InferVersions []uint32 `json:"infer_versions"` // infer protocol versions the buyer speaks; required
	Model         string   `json:"model"`
	BudgetMicros  uint64   `json:"budget"`
	MaxPriceIn    uint64   `json:"max_price_in,omitempty"` // 0 = no limit
	MaxPriceOut   uint64   `json:"max_price_out,omitempty"`
	Exclude       []string `json:"exclude,omitempty"` // nodes to skip after a failover
	Node          string   `json:"node,omitempty"`    // only this node (audits, pinned nodes)
	// PreferNode asks for this node when it is a candidate (online, not at
	// capacity, within the price limits), and for the usual choice otherwise:
	// the node that served a session's previous turn keeps the connection
	// warm and the model server's prompt cache.
	PreferNode string `json:"prefer_node,omitempty"`
	RequireTEE bool   `json:"require_tee,omitempty"` // only nodes whose quote the coordinator verified
	// Optional: the buyer's prompt estimate (EstimatePromptTokens) and the
	// request's max_tokens. With both set, the hold is sized to the worst-case
	// cost at the matched node's price instead of the whole budget, so a
	// balance supports many more requests in flight. Understating them only
	// truncates the buyer's own output.
	PromptTokens uint64 `json:"prompt_tokens,omitempty"`
	MaxTokens    uint64 `json:"max_tokens,omitempty"`
}

// TicketResponse carries the ticket and the node-signed offer it came from, so
// the buyer can check the node's keys, addresses and prices are the node's
// own and not substituted by the coordinator.
type TicketResponse struct {
	Ticket Ticket `json:"ticket"`
	Offer  Offer  `json:"offer"`
}

// InferRequest opens a job on the node. BinRequest frames follow it with the
// RequestBytes of the sealed request (SendSealedRequest):
// box.SealFrom(jobKey, NodeBoxPub, EncodeRequest(JSON), RequestAAD), which
// begins with BuyerBoxPub.
//
// The node answers with BinData frames: the response stream (see codec.go),
// sealed under the key both derive from the agreement of the per-job key and
// the node's box key (ResponseKeyInfo), each frame's sequence number its
// nonce and in its AAD (ChunkAAD). BinProgress frames keep a silent job
// alive. A receipt (or an error) ends the response.
//
// The node refuses requests whose sealed bytes do not begin with
// BuyerBoxPub. Because the receipt signs SHA-256 of the sealed request, it
// thereby commits to the response key, and disclosing that one private key
// later opens exactly this job for a dispute judge.
type InferRequest struct {
	Ticket       Ticket `json:"ticket"`
	BuyerBoxPub  []byte `json:"buyer_box_pub"` // per-job X25519 key; begins the sealed request
	RequestBytes uint64 `json:"request_bytes"` // length of the sealed request that follows
}

// ResumeRequest opens a new stream for a job whose stream broke (a relay
// restarted, the network changed). The node checks the stream comes from the
// job's buyer, and sends the response frames from From on (each with its
// number), then the live ones and the receipt, as if nothing had happened.
// A node keeps a broken job for a while (node.ResumeWindow); after that the
// job is settled as delivered so far.
type ResumeRequest struct {
	JobID string `json:"job_id"`
	From  uint64 `json:"from"` // the first response frame the buyer has not received
}

// Ack is the buyer's signature over AckBytes(receipt).
type Ack struct {
	Sig []byte `json:"sig"`
}

// Reject tells the node why the buyer refused its receipt.
type Reject struct {
	Reason string `json:"reason"`
}

// SettleRequest asks the coordinator to pay out a job.
type SettleRequest struct {
	Ticket  Ticket                `json:"ticket"`
	Receipt receipt.SignedReceipt `json:"receipt"`
	Ack     []byte                `json:"ack,omitempty"`
}

// SettleResponse reports the outcome of settlement.
type SettleResponse struct {
	Status     string `json:"status"` // "final" or "pending"
	CostMicros uint64 `json:"cost"`
	FeeMicros  uint64 `json:"fee"`
}

// ModelSummary aggregates current offers for one model.
type ModelSummary struct {
	Model         string `json:"model"`
	Nodes         int    `json:"nodes"`
	AttestedNodes int    `json:"attested_nodes,omitempty"` // nodes whose TEE quote the coordinator verified
	MinPriceIn    uint64 `json:"min_price_in"`
	MinPriceOut   uint64 `json:"min_price_out"`
	// SharedNodes counts the nodes of the asker's share partners among Nodes:
	// the asker uses them for free, before the market.
	SharedNodes int `json:"shared_nodes,omitempty"`
}

// ModelsResponse lists models with live offers.
type ModelsResponse struct {
	Models []ModelSummary `json:"models"`
}

// BalanceResponse is the caller's account.
type BalanceResponse struct {
	AvailableMicros int64 `json:"available"`
	HeldMicros      int64 `json:"held"`
}

// WithdrawRequest ends the sending node's offer before it expires. Announcements
// issued up to IssuedAt no longer count.
type WithdrawRequest struct {
	IssuedAt int64 `json:"issued_at"`
}

// AbandonRequest is a node giving up a ticketed job before delivering any
// output (busy, model server down). The coordinator releases the buyer's hold
// at once instead of at the ticket's deadline, which also frees the node's
// concurrency slot. Only the ticketed node may abandon, and only before it
// settled: abandoning forfeits payment, so a node gains nothing by lying.
//
// NoFault marks a job the buyer's request made impossible (the model server
// rejected it as invalid): the node is not penalized for it. A node that
// claims it falsely gains nothing (it is not paid), and buyers fail over to
// another node once before treating the request itself as bad.
type AbandonRequest struct {
	Ticket  Ticket `json:"ticket"`
	Reason  string `json:"reason"`
	NoFault bool   `json:"no_fault,omitempty"`
}

// StartedRequest is a node reporting that the buyer connected and the job is
// running. Held jobs never started by the time their ticket expires are
// released, so a buyer that takes tickets and never connects cannot keep a
// node's concurrency slots busy.
type StartedRequest struct {
	Ticket Ticket `json:"ticket"`
}

// DisputeRequest contests an unacknowledged settlement.
type DisputeRequest struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

// Evidence opens a dispute that the coordinator can decide on the facts. It is
// followed on the same stream by the RequestBytes of the sealed request
// exactly as sent (BinRequest frames, SendSealedRequest) and exactly Chunks
// BinData frames (the sealed response
// frames as received, in order), then the coordinator answers with a
// TypeVerdict frame.
//
// Disclosing JobSecret reveals this one job's prompt and response to the
// coordinator, and nothing else: every job uses a fresh key.
type Evidence struct {
	JobID     string                 `json:"job_id"`
	Reason    string                 `json:"reason"`
	JobSecret []byte                 `json:"job_secret"`        // the per-job X25519 private key
	Receipt   *receipt.SignedReceipt `json:"receipt,omitempty"` // as received from the node
	Chunks    uint64                 `json:"chunks"`
	// RequestBytes is the length of the sealed request that follows.
	RequestBytes uint64 `json:"request_bytes"`
}

// MaxEvidenceChunks bounds the response frames that may follow an Evidence frame.
const MaxEvidenceChunks = 1 << 18

// Verdict is the coordinator's decision on a dispute.
type Verdict struct {
	Outcome string `json:"outcome"` // upheld, dismissed, unverifiable, recorded
	Status  string `json:"status"`  // the job's settlement status afterwards
	Detail  string `json:"detail"`
}

// CheckReceipt verifies sr's signature and that it belongs to ticket t: signed
// by the ticketed node's receipt key, for this job and this model.
func CheckReceipt(t *Ticket, sr *receipt.SignedReceipt) error {
	if err := receipt.Verify(sr); err != nil {
		return Errorf(CodeBadRequest, "%v", err)
	}
	r := &sr.Receipt
	switch {
	case !bytes.Equal(r.NodeID, t.NodeReceiptKey):
		return Errorf(CodeBadRequest, "receipt not signed by the ticketed node")
	case r.JobID != t.JobID:
		return Errorf(CodeBadRequest, "receipt is for another job")
	case r.ModelID != t.Model:
		return Errorf(CodeBadRequest, "receipt model differs from ticket")
	}
	return nil
}
