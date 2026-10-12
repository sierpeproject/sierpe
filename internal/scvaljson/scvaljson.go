// Package scvaljson renders Soroban ScVal XDR as the JSON Stellar RPC serves
// under xdrFormat=json, in pure Go on top of the SDK's xdr and strkey
// packages. RPC produces that JSON with the Rust stellar-xdr serde through
// CGO, which CLAUDE.md rules 3 and 4 rule out here.
//
// The output is byte-identical to `stellar xdr decode --type ScVal --output
// json` (stellar-xdr 27.0.0) for every arm that crate defines; the golden
// vectors in testdata pin it. That holds for the bytes this package returns;
// an HTTP response written through encoding/json escapes & < > as \u0026
// \u003c \u003e, as RPC's does, so on the wire it matches the CLI as JSON,
// not byte for byte. Only the ScVal shape is promised: the serde output of
// other types (ContractEvent among them) has drifted between crate versions,
// so this package never renders them.
//
//	void, ledger_key_contract_instance   bare JSON strings
//	bool                                 {"bool":true}
//	u32, i32                             JSON numbers
//	u64, i64, timepoint, duration        decimal strings
//	u128, i128, u256, i256               decimal strings, signed where the type is
//	bytes                                lowercase hex
//	string, symbol                       escape_bytes (see appendEscaped)
//	vec                                  array, or null
//	map                                  [{"key":K,"val":V}], or null
//	address                              strkey G, C, M, B or L
//	error                                {"contract":N} or {"<type>":"<code>"}
//	contract_instance                    {"executable":{"wasm":hex}|"stellar_asset","storage":map|null}
//	ledger_key_nonce                     {"nonce":"<i64>"}
//
// Arms the SDK decodes but no released stellar-xdr defines yet (the
// executable tag value, the external-ref executable) are refused with an
// error rather than given a guessed shape that would later have to change.
package scvaljson

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// MaxDepth bounds recursion. The host caps object nesting far below it
// (anything a contract can store is shallower), and the XDR decoder's own
// limit is higher, so a value deeper than this is refused rather than
// walked.
const MaxDepth = 256

var (
	errDepth       = errors.New("scvaljson: nesting deeper than MaxDepth")
	errUnsupported = errors.New("scvaljson: arm not defined by any released stellar-xdr")
)

// FromBase64 decodes one base64 ScVal and renders it.
func FromBase64(s string) ([]byte, error) {
	var v xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(s, &v); err != nil {
		return nil, fmt.Errorf("scvaljson: decode: %w", err)
	}
	return Marshal(v)
}

// Marshal renders one ScVal. A malformed union (an arm pointer left nil,
// which panics on dereference here exactly as in the SDK's getters) comes
// back as an error: this is the recover frontier CLAUDE.md rule 6 asks for
// around every XDR walk.
func Marshal(v xdr.ScVal) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("scvaljson: malformed value: %v", r)
		}
	}()
	return appendVal(make([]byte, 0, 64), v, 0)
}

var errTypeNames = [...]string{
	"contract", "wasm_vm", "context", "storage", "object",
	"crypto", "events", "budget", "value", "auth",
}

var errCodeNames = [...]string{
	"arith_domain", "index_bounds", "invalid_input", "missing_value", "existing_value",
	"exceeded_limit", "invalid_action", "internal_error", "unexpected_type", "unexpected_size",
}

func appendVal(b []byte, v xdr.ScVal, depth int) ([]byte, error) {
	if depth > MaxDepth {
		return nil, errDepth
	}
	switch v.Type {
	case xdr.ScValTypeScvBool:
		if *v.B {
			return append(b, `{"bool":true}`...), nil
		}
		return append(b, `{"bool":false}`...), nil
	case xdr.ScValTypeScvVoid:
		return append(b, `"void"`...), nil
	case xdr.ScValTypeScvError:
		return appendError(b, *v.Error)
	case xdr.ScValTypeScvU32:
		return append(strconv.AppendUint(append(b, `{"u32":`...), uint64(*v.U32), 10), '}'), nil
	case xdr.ScValTypeScvI32:
		return append(strconv.AppendInt(append(b, `{"i32":`...), int64(*v.I32), 10), '}'), nil
	case xdr.ScValTypeScvU64:
		return closeStr(strconv.AppendUint(append(b, `{"u64":"`...), uint64(*v.U64), 10)), nil
	case xdr.ScValTypeScvI64:
		return closeStr(strconv.AppendInt(append(b, `{"i64":"`...), int64(*v.I64), 10)), nil
	case xdr.ScValTypeScvTimepoint:
		return closeStr(strconv.AppendUint(append(b, `{"timepoint":"`...), uint64(*v.Timepoint), 10)), nil
	case xdr.ScValTypeScvDuration:
		return closeStr(strconv.AppendUint(append(b, `{"duration":"`...), uint64(*v.Duration), 10)), nil
	case xdr.ScValTypeScvU128:
		n := fromWords(false, uint64(v.U128.Hi), uint64(v.U128.Lo))
		return closeStr(n.Append(append(b, `{"u128":"`...), 10)), nil
	case xdr.ScValTypeScvI128:
		n := fromWords(true, uint64(v.I128.Hi), uint64(v.I128.Lo))
		return closeStr(n.Append(append(b, `{"i128":"`...), 10)), nil
	case xdr.ScValTypeScvU256:
		p := v.U256
		n := fromWords(false, uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo))
		return closeStr(n.Append(append(b, `{"u256":"`...), 10)), nil
	case xdr.ScValTypeScvI256:
		p := v.I256
		n := fromWords(true, uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo))
		return closeStr(n.Append(append(b, `{"i256":"`...), 10)), nil
	case xdr.ScValTypeScvBytes:
		return closeStr(hex.AppendEncode(append(b, `{"bytes":"`...), *v.Bytes)), nil
	case xdr.ScValTypeScvString:
		return append(appendEscaped(append(b, `{"string":`...), []byte(*v.Str)), '}'), nil
	case xdr.ScValTypeScvSymbol:
		return append(appendEscaped(append(b, `{"symbol":`...), []byte(*v.Sym)), '}'), nil
	case xdr.ScValTypeScvVec:
		b = append(b, `{"vec":`...)
		if *v.Vec == nil {
			return append(b, `null}`...), nil
		}
		b = append(b, '[')
		for i, e := range **v.Vec {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = appendVal(b, e, depth+1); err != nil {
				return nil, err
			}
		}
		return append(b, `]}`...), nil
	case xdr.ScValTypeScvMap:
		var err error
		if b, err = appendMap(append(b, `{"map":`...), *v.Map, depth); err != nil {
			return nil, err
		}
		return append(b, '}'), nil
	case xdr.ScValTypeScvAddress:
		s, err := address(*v.Address)
		if err != nil {
			return nil, err
		}
		return closeStr(append(append(b, `{"address":"`...), s...)), nil
	case xdr.ScValTypeScvContractInstance:
		return appendInstance(b, *v.Instance, depth)
	case xdr.ScValTypeScvLedgerKeyContractInstance:
		return append(b, `"ledger_key_contract_instance"`...), nil
	case xdr.ScValTypeScvLedgerKeyNonce:
		b = strconv.AppendInt(append(b, `{"ledger_key_nonce":{"nonce":"`...), int64(v.NonceKey.Nonce), 10)
		return append(b, `"}}`...), nil
	case xdr.ScValTypeScvExecutableTag:
		return nil, fmt.Errorf("%w: ScVal type %d", errUnsupported, v.Type)
	}
	return nil, fmt.Errorf("scvaljson: unknown ScVal type %d", v.Type)
}

// appendMap renders map entries in stored order; a nil map is JSON null.
func appendMap(b []byte, m *xdr.ScMap, depth int) ([]byte, error) {
	if m == nil {
		return append(b, `null`...), nil
	}
	b = append(b, '[')
	for i, e := range *m {
		if i > 0 {
			b = append(b, ',')
		}
		var err error
		if b, err = appendVal(append(b, `{"key":`...), e.Key, depth+1); err != nil {
			return nil, err
		}
		if b, err = appendVal(append(b, `,"val":`...), e.Val, depth+1); err != nil {
			return nil, err
		}
		b = append(b, '}')
	}
	return append(b, ']'), nil
}

func appendInstance(b []byte, ci xdr.ScContractInstance, depth int) ([]byte, error) {
	b = append(b, `{"contract_instance":{"executable":`...)
	switch ci.Executable.Type {
	case xdr.ContractExecutableTypeContractExecutableWasm:
		b = closeStr(hex.AppendEncode(append(b, `{"wasm":"`...), ci.Executable.WasmHash[:]))
	case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		b = append(b, `"stellar_asset"`...)
	case xdr.ContractExecutableTypeContractExecutableExternalRef:
		return nil, fmt.Errorf("%w: contract executable type %d", errUnsupported, ci.Executable.Type)
	default:
		return nil, fmt.Errorf("scvaljson: unknown contract executable type %d", ci.Executable.Type)
	}
	b, err := appendMap(append(b, `,"storage":`...), ci.Storage, depth)
	if err != nil {
		return nil, err
	}
	return append(b, `}}`...), nil
}

func appendError(b []byte, e xdr.ScError) ([]byte, error) {
	t := int(e.Type)
	if t < 0 || t >= len(errTypeNames) {
		return nil, fmt.Errorf("scvaljson: unknown error type %d", t)
	}
	b = append(append(append(b, `{"error":{"`...), errTypeNames[t]...), `":`...)
	if e.Type == xdr.ScErrorTypeSceContract {
		b = strconv.AppendUint(b, uint64(*e.ContractCode), 10)
	} else {
		c := int(*e.Code)
		if c < 0 || c >= len(errCodeNames) {
			return nil, fmt.Errorf("scvaljson: unknown error code %d", c)
		}
		b = append(append(append(b, '"'), errCodeNames[c]...), '"')
	}
	return append(b, `}}`...), nil
}

// address renders any ScAddress arm as its strkey: G (account), C
// (contract), M (muxed account), B (claimable balance), L (liquidity pool).
func address(a xdr.ScAddress) (string, error) {
	switch a.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		k := a.AccountId.Ed25519
		return strkey.Encode(strkey.VersionByteAccountID, k[:])
	case xdr.ScAddressTypeScAddressTypeContract:
		return strkey.Encode(strkey.VersionByteContract, a.ContractId[:])
	case xdr.ScAddressTypeScAddressTypeMuxedAccount:
		m := a.MuxedAccount
		ma := xdr.MuxedAccount{
			Type:     xdr.CryptoKeyTypeKeyTypeMuxedEd25519,
			Med25519: &xdr.MuxedAccountMed25519{Id: m.Id, Ed25519: m.Ed25519},
		}
		return ma.GetAddress()
	case xdr.ScAddressTypeScAddressTypeClaimableBalance:
		return a.ClaimableBalanceId.EncodeToStrkey()
	case xdr.ScAddressTypeScAddressTypeLiquidityPool:
		return strkey.Encode(strkey.VersionByteLiquidityPool, a.LiquidityPoolId[:])
	}
	return "", fmt.Errorf("scvaljson: unknown address type %d", a.Type)
}

// fromWords assembles an integer from 64-bit words, most significant first.
// When signed, the top word is two's complement, as in the XDR parts types.
func fromWords(signed bool, words ...uint64) *big.Int {
	n := new(big.Int)
	if signed {
		n.SetInt64(int64(words[0]))
	} else {
		n.SetUint64(words[0])
	}
	w := new(big.Int)
	for _, x := range words[1:] {
		n.Lsh(n, 64)
		n.Add(n, w.SetUint64(x))
	}
	return n
}

func closeStr(b []byte) []byte { return append(b, `"}`...) }

// appendEscaped writes raw as a JSON string holding its escape_bytes
// rendering, which is how stellar-xdr serializes string and symbol:
// printable ASCII verbatim except the backslash, \0 \t \n \r named, and
// every other byte as \xNN in lowercase hex — valid UTF-8 included, so
// "señor" becomes "se\xc3\xb1or". That text is then JSON-escaped, where
// only the quote and the backslash can still occur. The escaping is
// lossless; a consumer that wants to display the string undoes it.
func appendEscaped(b []byte, raw []byte) []byte {
	const hexDigits = "0123456789abcdef"
	b = append(b, '"')
	for _, c := range raw {
		switch {
		case c == '\\':
			b = append(b, `\\\\`...)
		case c == '"':
			b = append(b, `\"`...)
		case c == 0:
			b = append(b, `\\0`...)
		case c == '\t':
			b = append(b, `\\t`...)
		case c == '\n':
			b = append(b, `\\n`...)
		case c == '\r':
			b = append(b, `\\r`...)
		case c >= 0x20 && c <= 0x7e:
			b = append(b, c)
		default:
			b = append(b, '\\', '\\', 'x', hexDigits[c>>4], hexDigits[c&15])
		}
	}
	return append(b, '"')
}
