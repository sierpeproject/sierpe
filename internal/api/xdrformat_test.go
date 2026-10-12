package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/zkCaleb-dev/sierpe/internal/store"
)

// fakeAPIMetrics counts what the handlers report. Handlers run on the test
// server's goroutines, hence the atomic.
type fakeAPIMetrics struct{ xdrJSONFailures atomic.Int64 }

func (f *fakeAPIMetrics) IncXDRJSONFailures() { f.xdrJSONFailures.Add(1) }

// Real mainnet values (escrow CCFTFEMD…, its first tw_fund and the USDC
// payment that funded it), each expected rendering copied from the
// reference: `stellar xdr decode --output json` (stellar-cli 27.1.0).
const (
	twFundTopic     = "AAAADwAAAAd0d19mdW5kAA=="
	twFundTopicJSON = `{"symbol":"tw_fund"}`
	twFundValue     = "AAAAEAAAAAEAAAACAAAACgAAAAAAAAAAAAAAAACYloAAAAASAAAAAAAAAACjg/mdwuZszEAC0vQk0M45n1ylpWaGiIuBuOMsA6BmAg=="
	twFundValueJSON = `{"vec":[{"i128":"10000000"},{"address":"GCRYH6M5YLTGZTCAALJPIJGQZY4Z6XFFUVTINCELQG4OGLADUBTAE3OU"}]}`

	instanceKey       = "AAAAFA=="
	instanceKeyJSON   = `"ledger_key_contract_instance"`
	instanceValue     = "AAAAEwAAAAAt217x+PAp6pMxPtemiG92Ptpyh9igi6zApCerCqHcHwAAAAA="
	instanceValueJSON = `{"contract_instance":{"executable":{"wasm":"2ddb5ef1f8f029ea93313ed7a6886f763eda7287d8a08bacc0a427ab0aa1dc1f"},"storage":null}}`
	escrowKey         = "AAAAEAAAAAEAAAABAAAADwAAAAZFc2Nyb3cAAA=="
	escrowKeyJSON     = `{"vec":[{"symbol":"Escrow"}]}`
	voidValue         = "AAAAAQ=="
	voidValueJSON     = `"void"`

	usdcSAC     = "CCW67TSZV3SSS2HXMBQ5JFGCKJNXKZM7UQUWUZPUTHXSTZLEO7SJMI75"
	usdcAsset   = "USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	usdcPayment = "AAAAAAAAAAGt785ZruUpaPdgYdSUwlJbdWWfpClqZfSZ7ynlZHfklgAAAAEAAAAAAAAABAAAAA8AAAAIdHJhbnNmZXIAAAASAAAAAAAAAACjg/mdwuZszEAC0vQk0M45n1ylpWaGiIuBuOMsA6BmAgAAABIAAAABizKRg3i/n+UKK6ixpTEdBYuPPDSqTQEYwn86wAiWkKgAAAAOAAAAPVVTREM6R0E1WlNFSllCMzdKUkM1QVZDSUE1TU9QNFJIVE0zMzVYMktHWDNJSE9KQVBQNVJFMzRLNEtaVk4AAAAAAAAKAAAAAAAAAAAAAAAAAJiWgA=="
	// usdcPaymentTopicsJSON and usdcPaymentValueJSON are body.v0.topics and
	// body.v0.data of the CLI's ContractEvent rendering of usdcPayment.
	usdcPaymentTopicsJSON = `[{"symbol":"transfer"},{"address":"GCRYH6M5YLTGZTCAALJPIJGQZY4Z6XFFUVTINCELQG4OGLADUBTAE3OU"},` +
		`{"address":"CCFTFEMDPC7Z7ZIKFOULDJJRDUCYXDZ4GSVE2AIYYJ7TVQAIS2IKRYEC"},` +
		`{"string":"USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"}]`
	usdcPaymentValueJSON = `{"i128":"10000000"}`

	// xlmSAC and xlmPayment: 1 XLM paid into a mainnet escrow by the native
	// asset's own contract.
	xlmSAC     = "CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA"
	xlmPayment = "AAAAAAAAAAEltPzYWa7C+mNIQ4xImzw8EMmLbSG+T9PLMMtolT75dwAAAAEAAAAAAAAABAAAAA8AAAAIdHJhbnNmZXIAAAASAAAAAAAAAABX3N3DRqlXTmtqTCTpvdMdy5KQD6bLuJlAKvMsL7OT4wAAABIAAAABf6pteQ2zqbBo9bF3CbbQhKrHLhMx1CfvcyoHusI9SqsAAAAOAAAABm5hdGl2ZQAAAAAACgAAAAAAAAAAAAAAAACYloA="

	// truncatedError is a ScVal that does not decode: an SCV_ERROR tag with
	// no body.
	truncatedError = "AAAAAg=="
)

type jsonFixtures struct {
	events    *fakeEventReader
	state     *fakeStateReader
	movements *fakeMovementReader
}

// newJSONTestAPI serves every endpoint that carries XDR, with a failure
// counter the test can read.
func newJSONTestAPI(f jsonFixtures, passphrase string) (*httptest.Server, *fakeAPIMetrics) {
	if f.events == nil {
		f.events = &fakeEventReader{}
	}
	f.events.cursor = store.Cursor{Sequence: 6000}
	if f.state == nil {
		f.state = &fakeStateReader{}
	}
	if f.movements == nil {
		f.movements = &fakeMovementReader{}
	}
	cr := defaultContractReader()
	cr.contract.Kinds = append(cr.contract.Kinds, store.KindMovements)
	cr.backfill.CoveredKinds = append(cr.backfill.CoveredKinds, store.KindMovements)

	metrics := &fakeAPIMetrics{}
	mux := http.NewServeMux()
	server := NewServer("testnet", f.events, cr, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.Register(mux)
	server.RegisterState(mux, f.state)
	server.RegisterMovements(mux, f.movements, passphrase)
	return httptest.NewServer(mux), metrics
}

func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, body
}

// rows decodes the page array under field into raw objects, so a test can
// tell an absent key from a null one.
func rows(t *testing.T, body []byte, field string) []map[string]json.RawMessage {
	t.Helper()
	var page map[string]json.RawMessage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, body)
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(page[field], &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", field, err, body)
	}
	return out
}

func expectRaw(t *testing.T, row map[string]json.RawMessage, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("%s is absent, want %s", key, want)
		return
	}
	if string(got) != want {
		t.Errorf("%s:\n got %s\nwant %s", key, got, want)
	}
}

func expectAbsent(t *testing.T, row map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if got, ok := row[key]; ok {
			t.Errorf("%s = %s, want it absent", key, got)
		}
	}
}

func twFundEvent() store.Event {
	e := sampleEvent("0254035113557024768-0000000001", 59147159)
	e.EventName, e.Topics, e.ValueXDR = "tw_fund", []string{twFundTopic}, twFundValue
	return e
}

func TestEventsXDRFormatJSON(t *testing.T) {
	srv, metrics := newJSONTestAPI(jsonFixtures{
		events: &fakeEventReader{events: []store.Event{twFundEvent()}},
	}, network.PublicNetworkPassphrase)
	defer srv.Close()

	code, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/events?xdrFormat=json")
	if code != 200 {
		t.Fatalf("status = %d (%s)", code, body)
	}
	e := rows(t, body, "events")[0]
	expectRaw(t, e, "topicJson", "["+twFundTopicJSON+"]")
	expectRaw(t, e, "valueJson", twFundValueJSON)
	// Additive: the base64 stays, because the topic filters and every
	// cross-check against the chain still speak base64.
	expectRaw(t, e, "topic", `["`+twFundTopic+`"]`)
	expectRaw(t, e, "value", `"`+twFundValue+`"`)
	if n := metrics.xdrJSONFailures.Load(); n != 0 {
		t.Errorf("failures counted = %d, want 0", n)
	}
}

// The default response must stay byte-identical to what clients already
// parse: no JSON view unless asked for, and base64 named explicitly is the
// same response as base64 by default.
func TestDefaultResponsesCarryNoJSONViews(t *testing.T) {
	srv, _ := newJSONTestAPI(jsonFixtures{
		events: &fakeEventReader{events: []store.Event{twFundEvent()}},
		state: &fakeStateReader{
			entries: []store.StateEntry{{ContractID: registered, KeyXDR: instanceKey, Durability: "persistent",
				ValueXDR: instanceValue, LastLedger: 5500, ClosedAt: time.Unix(1_700_000_000, 0).UTC()}},
			changes: []store.StateChange{{ID: "0000000000000000100-0000000000", ContractID: registered,
				LedgerSequence: 5500, ClosedAt: time.Unix(1_700_000_000, 0).UTC(), ChangeType: "updated",
				KeyXDR: escrowKey, Durability: "persistent", PreXDR: voidValue, PostXDR: voidValue}},
		},
		movements: &fakeMovementReader{movements: []store.Movement{sampleMovement("0000000000000000100-0000000000", store.RoleRecipient)}},
	}, network.TestNetworkPassphrase)
	defer srv.Close()

	for _, path := range []string{"/events", "/state", "/state/history", "/movements"} {
		base := srv.URL + "/v1/contracts/" + registered + path
		code, implicit := getBody(t, base)
		if code != 200 {
			t.Fatalf("%s: status = %d (%s)", path, code, implicit)
		}
		if bytes.Contains(implicit, []byte(`Json"`)) {
			t.Errorf("%s: default response carries a JSON view: %s", path, implicit)
		}
		if _, explicit := getBody(t, base+"?xdrFormat=base64"); !bytes.Equal(implicit, explicit) {
			t.Errorf("%s: xdrFormat=base64 differs from the default:\n%s\n%s", path, implicit, explicit)
		}
	}
}

func TestZeroTopicEventStillHasTopicJSON(t *testing.T) {
	e := twFundEvent()
	e.Topics = []string{}
	srv, _ := newJSONTestAPI(jsonFixtures{events: &fakeEventReader{events: []store.Event{e}}}, network.TestNetworkPassphrase)
	defer srv.Close()

	_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/events?xdrFormat=json")
	expectRaw(t, rows(t, body, "events")[0], "topicJson", "[]")
}

func TestXDRFormatRejectsUnknown(t *testing.T) {
	f := jsonFixtures{events: &fakeEventReader{}, state: &fakeStateReader{}, movements: &fakeMovementReader{}}
	srv, _ := newJSONTestAPI(f, network.TestNetworkPassphrase)
	defer srv.Close()

	for _, path := range []string{"/events", "/state", "/state/history", "/movements"} {
		code, body := getBody(t, srv.URL+"/v1/contracts/"+registered+path+"?xdrFormat=xml")
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, code)
		}
		if !strings.Contains(string(body), "xdrFormat") {
			t.Errorf("%s: error %s does not name the parameter", path, body)
		}
	}
	if len(f.events.queries)+len(f.state.queries)+len(f.movements.queries) != 0 {
		t.Error("invalid requests must never reach the store")
	}
}

// xdrFormat is presentation, not query: it is accepted beside a cursor, and
// it never changes the cursor a page mints.
func TestXDRFormatWithCursor(t *testing.T) {
	f := jsonFixtures{
		events: &fakeEventReader{events: []store.Event{twFundEvent()}, hasMore: true},
		state: &fakeStateReader{
			changes: []store.StateChange{{ID: "0000000000000000100-0000000000", ContractID: registered,
				LedgerSequence: 5500, ClosedAt: time.Unix(1_700_000_000, 0).UTC(), ChangeType: "created",
				KeyXDR: escrowKey, Durability: "persistent", PostXDR: voidValue}},
			entries: []store.StateEntry{{ContractID: registered, KeyXDR: instanceKey, Durability: "persistent",
				ValueXDR: instanceValue, LastLedger: 5500, ClosedAt: time.Unix(1_700_000_000, 0).UTC()}},
			hasMore: true,
		},
		movements: &fakeMovementReader{
			movements: []store.Movement{sampleMovement("0000000000000000100-0000000000", store.RoleRecipient)},
			hasMore:   true,
		},
	}
	srv, _ := newJSONTestAPI(f, network.TestNetworkPassphrase)
	defer srv.Close()

	for path, field := range map[string]string{
		"/events": "events", "/state": "entries", "/state/history": "changes", "/movements": "movements",
	} {
		base := srv.URL + "/v1/contracts/" + registered + path + "?limit=1"
		cursors := map[string]string{}
		for _, suffix := range []string{"", "&xdrFormat=json"} {
			var page struct{ Cursor string }
			if code := getJSON(t, base+suffix, &page); code != 200 {
				t.Fatalf("%s%s: status = %d", path, suffix, code)
			}
			cursors[suffix] = page.Cursor
		}
		if cursors[""] == "" || cursors[""] != cursors["&xdrFormat=json"] {
			t.Errorf("%s: cursor minted under json = %q, under base64 = %q; want the same, non-empty",
				path, cursors["&xdrFormat=json"], cursors[""])
		}
		url := srv.URL + "/v1/contracts/" + registered + path + "?cursor=" + cursors[""] + "&xdrFormat=json"
		code, body := getBody(t, url)
		if code != 200 {
			t.Fatalf("%s: cursor plus xdrFormat: status = %d (%s)", path, code, body)
		}
		if !bytes.Contains(body, []byte(`Json"`)) || len(rows(t, body, field)) == 0 {
			t.Errorf("%s: cursor page under xdrFormat=json has no JSON view: %s", path, body)
		}
	}
}

func TestStateSnapshotJSON(t *testing.T) {
	srv, _ := newJSONTestAPI(jsonFixtures{state: &fakeStateReader{
		entries: []store.StateEntry{{ContractID: registered, KeyXDR: instanceKey, Durability: "persistent",
			ValueXDR: instanceValue, LastLedger: 5500, ClosedAt: time.Unix(1_700_000_000, 0).UTC()}},
	}}, network.TestNetworkPassphrase)
	defer srv.Close()

	_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/state?xdrFormat=json")
	e := rows(t, body, "entries")[0]
	expectRaw(t, e, "keyJson", instanceKeyJSON)
	expectRaw(t, e, "valueJson", instanceValueJSON)
	expectRaw(t, e, "key", `"`+instanceKey+`"`)
}

// Absent means there is no value; a JSON view never invents one.
func TestStateHistoryJSON(t *testing.T) {
	change := func(id, changeType, pre, post string) store.StateChange {
		return store.StateChange{ID: id, ContractID: registered, LedgerSequence: 5500,
			ClosedAt: time.Unix(1_700_000_000, 0).UTC(), ChangeType: changeType,
			KeyXDR: escrowKey, Durability: "persistent", PreXDR: pre, PostXDR: post}
	}
	srv, _ := newJSONTestAPI(jsonFixtures{state: &fakeStateReader{changes: []store.StateChange{
		change("0000000000000000100-0000000000", "created", "", instanceValue),
		change("0000000000000000101-0000000000", "updated", instanceValue, voidValue),
		change("0000000000000000102-0000000000", "removed", voidValue, ""),
	}}}, network.TestNetworkPassphrase)
	defer srv.Close()

	_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/state/history?xdrFormat=json")
	changes := rows(t, body, "changes")
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(changes))
	}
	created, updated, removed := changes[0], changes[1], changes[2]
	for _, c := range changes {
		expectRaw(t, c, "keyJson", escrowKeyJSON)
	}
	expectAbsent(t, created, "pre", "preJson")
	expectRaw(t, created, "postJson", instanceValueJSON)
	expectRaw(t, updated, "preJson", instanceValueJSON)
	expectRaw(t, updated, "postJson", voidValueJSON)
	expectRaw(t, removed, "preJson", voidValueJSON)
	expectAbsent(t, removed, "post", "postJson")
}

// A value that exists but cannot be rendered is null, its base64 intact,
// and counted. The body must not be empty: writeJSON sends the 200 before
// encoding, so one invalid raw value would have truncated the response to
// nothing.
func TestUndecodableValueIsNullAndCounted(t *testing.T) {
	srv, metrics := newJSONTestAPI(jsonFixtures{state: &fakeStateReader{changes: []store.StateChange{{
		ID: "0000000000000000100-0000000000", ContractID: registered, LedgerSequence: 5500,
		ClosedAt: time.Unix(1_700_000_000, 0).UTC(), ChangeType: "updated",
		KeyXDR: escrowKey, Durability: "persistent", PreXDR: voidValue, PostXDR: truncatedError,
	}}}}, network.TestNetworkPassphrase)
	defer srv.Close()

	code, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/state/history?xdrFormat=json")
	if code != 200 || len(body) == 0 {
		t.Fatalf("status = %d, body = %q: the page must survive one bad value", code, body)
	}
	c := rows(t, body, "changes")[0]
	expectRaw(t, c, "postJson", "null")
	expectRaw(t, c, "post", `"`+truncatedError+`"`)
	expectRaw(t, c, "preJson", voidValueJSON)
	if n := metrics.xdrJSONFailures.Load(); n != 1 {
		t.Errorf("failures counted = %d, want 1", n)
	}
}

func paymentMovement(token, rawXDR string) store.Movement {
	m := sampleMovement("0254035113557024768-0000000000", store.RoleRecipient)
	m.TokenContractID, m.Amount, m.RawXDR = token, "10000000", rawXDR
	return m
}

func TestMovementsJSON(t *testing.T) {
	srv, metrics := newJSONTestAPI(jsonFixtures{movements: &fakeMovementReader{
		movements: []store.Movement{paymentMovement(usdcSAC, usdcPayment)},
	}}, network.PublicNetworkPassphrase)
	defer srv.Close()

	_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/movements?xdrFormat=json")
	m := rows(t, body, "movements")[0]
	expectRaw(t, m, "topicJson", usdcPaymentTopicsJSON)
	expectRaw(t, m, "valueJson", usdcPaymentValueJSON)
	expectRaw(t, m, "rawXdr", `"`+usdcPayment+`"`)
	if n := metrics.xdrJSONFailures.Load(); n != 0 {
		t.Errorf("failures counted = %d, want 0", n)
	}
}

// A stored event that does not decode keeps its bytes and yields two nulls,
// one per field, each counted.
func TestUndecodableMovementEventIsNullAndCounted(t *testing.T) {
	srv, metrics := newJSONTestAPI(jsonFixtures{movements: &fakeMovementReader{
		movements: []store.Movement{sampleMovement("0000000000000000100-0000000000", store.RoleRecipient)},
	}}, network.TestNetworkPassphrase)
	defer srv.Close()

	code, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/movements?xdrFormat=json")
	if code != 200 {
		t.Fatalf("status = %d (%s)", code, body)
	}
	m := rows(t, body, "movements")[0]
	expectRaw(t, m, "topicJson", "null")
	expectRaw(t, m, "valueJson", "null")
	expectRaw(t, m, "rawXdr", `"cmF3LWV2ZW50LXhkcg=="`)
	expectAbsent(t, m, "asset", "decimals")
	if n := metrics.xdrJSONFailures.Load(); n != 2 {
		t.Errorf("failures counted = %d, want 2", n)
	}
}

func symbolVal(s string) xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

func stringVal(s string) xdr.ScVal {
	str := xdr.ScString(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &str}
}

func contractVal(t *testing.T, id string) xdr.ScVal {
	raw, err := strkey.Decode(strkey.VersionByteContract, id)
	if err != nil {
		t.Fatal(err)
	}
	var cid xdr.ContractId
	copy(cid[:], raw)
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
		Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid}}
}

// transferEvent builds the base64 ContractEvent of a transfer emitted by
// emitter into the registered contract, whose trailing topic names asset.
func transferEvent(t *testing.T, emitter, asset string) string {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, emitter)
	if err != nil {
		t.Fatal(err)
	}
	var id xdr.ContractId
	copy(id[:], raw)
	amount := xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Lo: 10000000}}
	ev := xdr.ContractEvent{ContractId: &id, Type: xdr.ContractEventTypeContract, Body: xdr.ContractEventBody{
		V: 0,
		V0: &xdr.ContractEventV0{
			Topics: []xdr.ScVal{symbolVal("transfer"), contractVal(t, xlmSAC), contractVal(t, registered), stringVal(asset)},
			Data:   amount,
		},
	}}
	b64, err := xdr.MarshalBase64(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b64
}

// The asset string in a transfer is chosen by whoever emits it. A movement
// is labelled only when the asset's contract id, derived under this
// instance's passphrase, is the emitter — in either format.
func TestMovementsSACAssetIsProvenOrAbsent(t *testing.T) {
	custom := "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	cases := []struct {
		name, token, rawXDR, passphrase, want string
	}{
		{"USDC paid by its SAC", usdcSAC, usdcPayment, network.PublicNetworkPassphrase, usdcAsset},
		{"XLM paid by the native SAC", xlmSAC, xlmPayment, network.PublicNetworkPassphrase, "native"},
		{"right token, other network", usdcSAC, usdcPayment, network.TestNetworkPassphrase, ""},
		{"custom token naming USDC", custom, transferEvent(t, custom, usdcAsset), network.PublicNetworkPassphrase, ""},
		{"stored token disagrees with the event", xlmSAC, usdcPayment, network.PublicNetworkPassphrase, ""},
		{"comma list", usdcSAC, transferEvent(t, usdcSAC, usdcAsset+",native"), network.PublicNetworkPassphrase, ""},
		{"extra colon", usdcSAC, transferEvent(t, usdcSAC, usdcAsset+":x"), network.PublicNetworkPassphrase, ""},
		{"legacy row without the event", usdcSAC, "", network.PublicNetworkPassphrase, ""},
		{"undecodable event", usdcSAC, "cmF3LWV2ZW50LXhkcg==", network.PublicNetworkPassphrase, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newJSONTestAPI(jsonFixtures{movements: &fakeMovementReader{
				movements: []store.Movement{paymentMovement(tc.token, tc.rawXDR)},
			}}, tc.passphrase)
			defer srv.Close()

			for _, format := range []string{"", "?xdrFormat=json"} {
				_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/movements"+format)
				m := rows(t, body, "movements")[0]
				if tc.want == "" {
					expectAbsent(t, m, "asset", "decimals")
					continue
				}
				expectRaw(t, m, "asset", `"`+tc.want+`"`)
				expectRaw(t, m, "decimals", "7")
			}
		})
	}
}

// A legacy row has no event to render: absent, not null, and not counted.
func TestLegacyMovementHasNoJSONView(t *testing.T) {
	srv, metrics := newJSONTestAPI(jsonFixtures{movements: &fakeMovementReader{
		movements: []store.Movement{paymentMovement(usdcSAC, "")},
	}}, network.PublicNetworkPassphrase)
	defer srv.Close()

	_, body := getBody(t, srv.URL+"/v1/contracts/"+registered+"/movements?xdrFormat=json")
	expectAbsent(t, rows(t, body, "movements")[0], "rawXdr", "topicJson", "valueJson")
	if n := metrics.xdrJSONFailures.Load(); n != 0 {
		t.Errorf("failures counted = %d, want 0", n)
	}
}
