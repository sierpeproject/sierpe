package api

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/zkCaleb-dev/sierpe/internal/scvaljson"
)

// apiMetrics is the instrument slice the public API feeds.
type apiMetrics interface {
	IncXDRJSONFailures()
}

// jsonNull is what a *Json field carries when its value exists but could
// not be rendered: the base64 beside it is still served, intact.
var jsonNull = json.RawMessage("null")

// xdrFormatParam reads the optional xdrFormat parameter with the RPC's own
// validation (same name, same values, same message), and reports whether
// the JSON views were asked for.
//
// It is a presentation parameter: it changes how a page is written, never
// which rows it holds, so it is accepted beside a cursor and never encoded
// in one — a client repeats it on every page, exactly as with RPC.
func xdrFormatParam(params url.Values) (bool, error) {
	format := params.Get("xdrFormat")
	if err := protocol.IsValidFormat(format); err != nil {
		return false, err
	}
	return format == protocol.FormatJSON, nil
}

// scvalJSON renders one stored base64 ScVal.
func (s *Server) scvalJSON(b64 string) json.RawMessage {
	return s.checked(scvaljson.FromBase64(b64))
}

// renderScVal renders one ScVal that is already decoded.
func (s *Server) renderScVal(v xdr.ScVal) json.RawMessage {
	return s.checked(scvaljson.Marshal(v))
}

// checked lets a rendering through only when it is valid JSON. Anything the
// encoder refuses, or anything that would not parse, is served as null and
// counted: one invalid json.RawMessage makes the response encoder fail
// after the 200 is written, which reaches the client as an empty body.
func (s *Server) checked(out []byte, err error) json.RawMessage {
	if err == nil && json.Valid(out) {
		return out
	}
	if err == nil {
		err = errors.New("encoder output is not valid JSON")
	}
	return s.unrendered(err)
}

// unrendered records one value served as null instead of its JSON view.
func (s *Server) unrendered(err error) json.RawMessage {
	s.metrics.IncXDRJSONFailures()
	s.log.Warn("xdr value served without its json view", "err", err)
	return jsonNull
}

// topicsJSON renders an event's topics as one JSON array: always an array,
// empty for an event without topics, with a null element for a topic that
// could not be rendered.
func (s *Server) topicsJSON(topics []string) json.RawMessage {
	out := []byte{'['}
	for i, t := range topics {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, s.scvalJSON(t)...)
	}
	return append(out, ']')
}

// movementEvent is what the movement views read from a stored
// ContractEvent: who emitted it, and its topics and data.
type movementEvent struct {
	emitter string
	body    xdr.ContractEventV0
}

// decodeMovementEvent reads a movement's stored ContractEvent. ok is false
// when the bytes do not decode; callers check for absent bytes (rows
// ingested before migration 0011) first, because absent is not a failure.
// The union arms are checked by hand rather than through the SDK getters,
// which dereference a nil arm (rule 6).
func decodeMovementEvent(rawXDR string) (movementEvent, bool) {
	var ev xdr.ContractEvent
	if err := xdr.SafeUnmarshalBase64(rawXDR, &ev); err != nil || ev.Body.V != 0 || ev.Body.V0 == nil {
		return movementEvent{}, false
	}
	out := movementEvent{body: *ev.Body.V0}
	if ev.ContractId != nil {
		out.emitter, _ = strkey.Encode(strkey.VersionByteContract, ev.ContractId[:])
	}
	return out, true
}

// movementJSON renders the topics and data of the event a movement was
// decoded from. Only those ScVals are rendered, never the ContractEvent
// envelope: its serde shape has already changed between stellar-xdr
// versions, and the ScVal shape is the one Sierpe promises. An event that
// cannot be read yields two nulls, one per field, each counted.
func (s *Server) movementJSON(ev movementEvent, decoded bool) (topics, value json.RawMessage) {
	if !decoded {
		err := errors.New("stored movement event does not decode")
		return s.unrendered(err), s.unrendered(err)
	}
	topics = []byte{'['}
	for i, t := range ev.body.Topics {
		if i > 0 {
			topics = append(topics, ',')
		}
		topics = append(topics, s.renderScVal(t)...)
	}
	return append(topics, ']'), s.renderScVal(ev.body.Data)
}

// sacDecimals is the precision of every Stellar Asset Contract: it wraps a
// classic asset, whose amounts are int64 stroops, so CAP-46-6 fixes it at 7.
const sacDecimals = 7

// assetCode is the SEP-0011 code alphabet: 1 to 12 ASCII alphanumerics.
var assetCode = regexp.MustCompile(`^[A-Za-z0-9]{1,12}$`)

// sacAssets proves which movements were emitted by a Stellar Asset
// Contract, and of which asset. A SAC names its asset in the trailing
// string topic of every transfer, but so can any custom token, so the
// string alone proves nothing: it is believed only when the contract id it
// hashes to under this instance's passphrase is the emitter itself. Results
// are memoized for the lifetime of one request.
type sacAssets struct {
	passphrase string
	proven     map[[2]string]bool
}

func newSACAssets(passphrase string) *sacAssets {
	return &sacAssets{passphrase: passphrase, proven: map[[2]string]bool{}}
}

// assetOf returns the SEP-0011 asset of the event when its emitter is
// proven to be that asset's SAC, and "" otherwise.
func (a *sacAssets) assetOf(ev movementEvent, token string) string {
	// The stored token id and the stored event must agree on the emitter
	// before the event's claims are weighed at all.
	topics := ev.body.Topics
	if ev.emitter == "" || ev.emitter != token || len(topics) == 0 {
		return ""
	}
	last := topics[len(topics)-1]
	if last.Type != xdr.ScValTypeScvString || last.Str == nil {
		return ""
	}
	name := string(*last.Str)
	key := [2]string{name, token}
	proven, seen := a.proven[key]
	if !seen {
		proven = a.derivesTo(name, token)
		a.proven[key] = proven
	}
	if !proven {
		return ""
	}
	return name
}

// derivesTo parses name strictly as one SEP-0011 asset ("native" or
// CODE:ISSUER) and reports whether its SAC id is token. xdr.BuildAssets is
// deliberately not used: it splits on commas, so "USDC:G...,native" would
// parse.
func (a *sacAssets) derivesTo(name, token string) bool {
	var asset xdr.Asset
	if name == "native" {
		asset = xdr.MustNewNativeAsset()
	} else {
		code, issuer, found := strings.Cut(name, ":")
		if !found || strings.Contains(issuer, ":") || !assetCode.MatchString(code) ||
			!strkey.IsValidEd25519PublicKey(issuer) {
			return false
		}
		var err error
		if asset, err = xdr.NewCreditAsset(code, issuer); err != nil {
			return false
		}
	}
	id, err := asset.ContractID(a.passphrase)
	if err != nil {
		return false
	}
	derived, err := strkey.Encode(strkey.VersionByteContract, id[:])
	return err == nil && derived == token
}
