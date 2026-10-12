package scvaljson

import (
	"bufio"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

type vector struct{ name, b64, want string }

// loadGolden reads testdata/golden.tsv. Its expected column comes from the
// reference implementations (stellar CLI, Stellar RPC), never from this
// package; the file header states the provenance of each group.
func loadGolden(tb testing.TB) []vector {
	tb.Helper()
	f, err := os.Open("testdata/golden.tsv")
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	var out []vector
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 3 {
			tb.Fatalf("golden line %q: want 3 tab-separated columns, got %d", line, len(cols))
		}
		out = append(out, vector{cols[0], cols[1], cols[2]})
	}
	if err := sc.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// Byte equality with the reference, not semantic equality: consumers that
// already parse RPC's xdrFormat=json must be able to parse Sierpe's without
// knowing which server answered.
func TestGolden(t *testing.T) {
	vectors := loadGolden(t)
	if len(vectors) < 250 {
		t.Fatalf("golden file holds %d vectors; a truncated file would pass vacuously", len(vectors))
	}
	for _, v := range vectors {
		got, err := FromBase64(v.b64)
		if err != nil {
			t.Errorf("%s: %v", v.name, err)
			continue
		}
		if string(got) != v.want {
			t.Errorf("%s:\n got %s\nwant %s", v.name, got, v.want)
		}
	}
}

func contractAddress(b byte) xdr.ScAddress {
	var id xdr.ContractId
	id[0] = b
	return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id}
}

// renderable holds one constructed value per ScVal arm this package renders.
func renderable() map[xdr.ScValType]xdr.ScVal {
	t := true
	u32, i32 := xdr.Uint32(7), xdr.Int32(-7)
	u64, i64 := xdr.Uint64(7), xdr.Int64(-7)
	tp, dur := xdr.TimePoint(1), xdr.Duration(2)
	code := xdr.Uint32(3)
	bytes, str, sym := xdr.ScBytes{0xde, 0xad}, xdr.ScString("s"), xdr.ScSymbol("s")
	vec := &xdr.ScVec{}
	m := &xdr.ScMap{}
	addr := contractAddress(1)
	return map[xdr.ScValType]xdr.ScVal{
		xdr.ScValTypeScvBool:      {Type: xdr.ScValTypeScvBool, B: &t},
		xdr.ScValTypeScvVoid:      {Type: xdr.ScValTypeScvVoid},
		xdr.ScValTypeScvError:     {Type: xdr.ScValTypeScvError, Error: &xdr.ScError{Type: xdr.ScErrorTypeSceContract, ContractCode: &code}},
		xdr.ScValTypeScvU32:       {Type: xdr.ScValTypeScvU32, U32: &u32},
		xdr.ScValTypeScvI32:       {Type: xdr.ScValTypeScvI32, I32: &i32},
		xdr.ScValTypeScvU64:       {Type: xdr.ScValTypeScvU64, U64: &u64},
		xdr.ScValTypeScvI64:       {Type: xdr.ScValTypeScvI64, I64: &i64},
		xdr.ScValTypeScvTimepoint: {Type: xdr.ScValTypeScvTimepoint, Timepoint: &tp},
		xdr.ScValTypeScvDuration:  {Type: xdr.ScValTypeScvDuration, Duration: &dur},
		xdr.ScValTypeScvU128:      {Type: xdr.ScValTypeScvU128, U128: &xdr.UInt128Parts{Hi: 1, Lo: 2}},
		xdr.ScValTypeScvI128:      {Type: xdr.ScValTypeScvI128, I128: &xdr.Int128Parts{Hi: -1, Lo: 2}},
		xdr.ScValTypeScvU256:      {Type: xdr.ScValTypeScvU256, U256: &xdr.UInt256Parts{LoLo: 1}},
		xdr.ScValTypeScvI256:      {Type: xdr.ScValTypeScvI256, I256: &xdr.Int256Parts{HiHi: -1}},
		xdr.ScValTypeScvBytes:     {Type: xdr.ScValTypeScvBytes, Bytes: &bytes},
		xdr.ScValTypeScvString:    {Type: xdr.ScValTypeScvString, Str: &str},
		xdr.ScValTypeScvSymbol:    {Type: xdr.ScValTypeScvSymbol, Sym: &sym},
		xdr.ScValTypeScvVec:       {Type: xdr.ScValTypeScvVec, Vec: &vec},
		xdr.ScValTypeScvMap:       {Type: xdr.ScValTypeScvMap, Map: &m},
		xdr.ScValTypeScvAddress:   {Type: xdr.ScValTypeScvAddress, Address: &addr},
		xdr.ScValTypeScvContractInstance: {Type: xdr.ScValTypeScvContractInstance, Instance: &xdr.ScContractInstance{
			Executable: xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableStellarAsset},
		}},
		xdr.ScValTypeScvLedgerKeyContractInstance: {Type: xdr.ScValTypeScvLedgerKeyContractInstance},
		xdr.ScValTypeScvLedgerKeyNonce:            {Type: xdr.ScValTypeScvLedgerKeyNonce, NonceKey: &xdr.ScNonceKey{Nonce: 9}},
	}
}

// The SDK is allowed to run ahead of the network: v0.7.2 already decodes
// arms no released stellar-xdr defines. Every arm the SDK can hand us must
// either render or be refused on purpose, so an SDK bump that adds one fails
// here instead of turning live values into nulls nobody expected.
func TestEveryScValTypeIsHandled(t *testing.T) {
	tag := xdr.ScString("tag")
	refused := map[xdr.ScValType]xdr.ScVal{
		xdr.ScValTypeScvExecutableTag: {Type: xdr.ScValTypeScvExecutableTag, ExecutableTag: &tag},
	}
	samples := renderable()
	for i := int32(0); i < 64; i++ {
		if !xdr.ScValType(0).ValidEnum(i) {
			continue
		}
		typ := xdr.ScValType(i)
		if v, ok := refused[typ]; ok {
			if _, err := Marshal(v); !errors.Is(err, errUnsupported) {
				t.Errorf("%s: err = %v, want the refusal", typ, err)
			}
			continue
		}
		v, ok := samples[typ]
		if !ok {
			t.Errorf("%s (%d) has no rendering: the SDK gained an arm. Render it with the shape a released "+
				"stellar-xdr gives it (and add golden vectors), or refuse it until one defines it", typ, i)
			continue
		}
		out, err := Marshal(v)
		if err != nil || !json.Valid(out) {
			t.Errorf("%s: out = %s, err = %v", typ, out, err)
		}
	}
}

func TestEveryExecutableTypeIsHandled(t *testing.T) {
	var wasm xdr.Hash
	for i := int32(0); i < 64; i++ {
		if !xdr.ContractExecutableType(0).ValidEnum(i) {
			continue
		}
		exec := xdr.ContractExecutable{Type: xdr.ContractExecutableType(i)}
		refuse := false
		switch exec.Type {
		case xdr.ContractExecutableTypeContractExecutableWasm:
			exec.WasmHash = &wasm
		case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		case xdr.ContractExecutableTypeContractExecutableExternalRef:
			exec.ExternalRef = &xdr.ContractExecutableExternalRef{ExecutableOwner: contractAddress(2), Tag: "tag"}
			refuse = true
		default:
			t.Errorf("contract executable type %d has no rendering: the SDK gained an arm", i)
			continue
		}
		v := xdr.ScVal{Type: xdr.ScValTypeScvContractInstance, Instance: &xdr.ScContractInstance{Executable: exec}}
		out, err := Marshal(v)
		switch {
		case refuse && !errors.Is(err, errUnsupported):
			t.Errorf("%s: err = %v, want the refusal", exec.Type, err)
		case !refuse && (err != nil || !json.Valid(out)):
			t.Errorf("%s: out = %s, err = %v", exec.Type, out, err)
		}
	}
}

func TestEveryAddressAndErrorKindIsHandled(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := int32(0); i < 64; i++ {
		if !xdr.ScAddressType(0).ValidEnum(i) {
			continue
		}
		a := randomAddress(r, xdr.ScAddressType(i))
		if _, err := address(a); err != nil {
			t.Errorf("address type %d: %v", i, err)
		}
	}
	for i := int32(0); i < 64; i++ {
		if !xdr.ScErrorType(0).ValidEnum(i) {
			continue
		}
		for c := int32(0); c < 64; c++ {
			if !xdr.ScErrorCode(0).ValidEnum(c) {
				continue
			}
			e := xdr.ScError{Type: xdr.ScErrorType(i)}
			code, contractCode := xdr.ScErrorCode(c), xdr.Uint32(c)
			if e.Type == xdr.ScErrorTypeSceContract {
				e.ContractCode = &contractCode
			} else {
				e.Code = &code
			}
			if out, err := Marshal(xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &e}); err != nil || !json.Valid(out) {
				t.Errorf("error type %d code %d: out = %s, err = %v", i, c, out, err)
			}
		}
	}
}

func randomAddress(r *rand.Rand, typ xdr.ScAddressType) xdr.ScAddress {
	var raw [32]byte
	for i := range raw {
		raw[i] = byte(r.UintN(256))
	}
	a := xdr.ScAddress{Type: typ}
	switch typ {
	case xdr.ScAddressTypeScAddressTypeAccount:
		k := xdr.Uint256(raw)
		a.AccountId = &xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &k}
	case xdr.ScAddressTypeScAddressTypeContract:
		id := xdr.ContractId(raw)
		a.ContractId = &id
	case xdr.ScAddressTypeScAddressTypeMuxedAccount:
		a.MuxedAccount = &xdr.MuxedEd25519Account{Id: xdr.Uint64(r.Uint64()), Ed25519: xdr.Uint256(raw)}
	case xdr.ScAddressTypeScAddressTypeClaimableBalance:
		h := xdr.Hash(raw)
		a.ClaimableBalanceId = &xdr.ClaimableBalanceId{Type: xdr.ClaimableBalanceIdTypeClaimableBalanceIdTypeV0, V0: &h}
	case xdr.ScAddressTypeScAddressTypeLiquidityPool:
		id := xdr.PoolId(raw)
		a.LiquidityPoolId = &id
	}
	return a
}

func randomBytes(r *rand.Rand) []byte {
	b := make([]byte, r.IntN(24))
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

// randomScVal builds a well-formed value of any renderable arm, nested up
// to depth levels, with arbitrary bytes in strings and symbols.
func randomScVal(r *rand.Rand, depth int) xdr.ScVal {
	for {
		typ := xdr.ScValType(r.IntN(int(xdr.ScValTypeScvLedgerKeyNonce) + 1))
		composite := typ == xdr.ScValTypeScvVec || typ == xdr.ScValTypeScvMap || typ == xdr.ScValTypeScvContractInstance
		if depth == 0 && composite {
			continue
		}
		v := xdr.ScVal{Type: typ}
		switch typ {
		case xdr.ScValTypeScvBool:
			b := r.IntN(2) == 1
			v.B = &b
		case xdr.ScValTypeScvError:
			e := xdr.ScError{Type: xdr.ScErrorType(r.IntN(10))}
			if e.Type == xdr.ScErrorTypeSceContract {
				c := xdr.Uint32(r.Uint32())
				e.ContractCode = &c
			} else {
				c := xdr.ScErrorCode(r.IntN(10))
				e.Code = &c
			}
			v.Error = &e
		case xdr.ScValTypeScvU32:
			n := xdr.Uint32(r.Uint32())
			v.U32 = &n
		case xdr.ScValTypeScvI32:
			n := xdr.Int32(r.Uint32())
			v.I32 = &n
		case xdr.ScValTypeScvU64:
			n := xdr.Uint64(r.Uint64())
			v.U64 = &n
		case xdr.ScValTypeScvI64:
			n := xdr.Int64(r.Uint64())
			v.I64 = &n
		case xdr.ScValTypeScvTimepoint:
			n := xdr.TimePoint(r.Uint64())
			v.Timepoint = &n
		case xdr.ScValTypeScvDuration:
			n := xdr.Duration(r.Uint64())
			v.Duration = &n
		case xdr.ScValTypeScvU128:
			v.U128 = &xdr.UInt128Parts{Hi: xdr.Uint64(r.Uint64()), Lo: xdr.Uint64(r.Uint64())}
		case xdr.ScValTypeScvI128:
			v.I128 = &xdr.Int128Parts{Hi: xdr.Int64(r.Uint64()), Lo: xdr.Uint64(r.Uint64())}
		case xdr.ScValTypeScvU256:
			v.U256 = &xdr.UInt256Parts{HiHi: xdr.Uint64(r.Uint64()), HiLo: xdr.Uint64(r.Uint64()),
				LoHi: xdr.Uint64(r.Uint64()), LoLo: xdr.Uint64(r.Uint64())}
		case xdr.ScValTypeScvI256:
			v.I256 = &xdr.Int256Parts{HiHi: xdr.Int64(r.Uint64()), HiLo: xdr.Uint64(r.Uint64()),
				LoHi: xdr.Uint64(r.Uint64()), LoLo: xdr.Uint64(r.Uint64())}
		case xdr.ScValTypeScvBytes:
			b := xdr.ScBytes(randomBytes(r))
			v.Bytes = &b
		case xdr.ScValTypeScvString:
			s := xdr.ScString(randomBytes(r))
			v.Str = &s
		case xdr.ScValTypeScvSymbol:
			s := xdr.ScSymbol(randomBytes(r))
			v.Sym = &s
		case xdr.ScValTypeScvVec:
			var vec *xdr.ScVec
			if r.IntN(5) > 0 {
				vec = &xdr.ScVec{}
				for range r.IntN(4) {
					*vec = append(*vec, randomScVal(r, depth-1))
				}
			}
			v.Vec = &vec
		case xdr.ScValTypeScvMap:
			m := randomMap(r, depth)
			v.Map = &m
		case xdr.ScValTypeScvAddress:
			a := randomAddress(r, xdr.ScAddressType(r.IntN(5)))
			v.Address = &a
		case xdr.ScValTypeScvContractInstance:
			ci := xdr.ScContractInstance{Storage: randomMap(r, depth)}
			if r.IntN(2) == 0 {
				var h xdr.Hash
				copy(h[:], randomBytes(r))
				ci.Executable = xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableWasm, WasmHash: &h}
			} else {
				ci.Executable = xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableStellarAsset}
			}
			v.Instance = &ci
		case xdr.ScValTypeScvLedgerKeyNonce:
			v.NonceKey = &xdr.ScNonceKey{Nonce: xdr.Int64(r.Uint64())}
		}
		return v
	}
}

func randomMap(r *rand.Rand, depth int) *xdr.ScMap {
	if r.IntN(5) == 0 {
		return nil
	}
	m := xdr.ScMap{}
	for range r.IntN(4) {
		m = append(m, xdr.ScMapEntry{Key: randomScVal(r, depth-1), Val: randomScVal(r, depth-1)})
	}
	return &m
}

// Whatever the value, the output is JSON: an invalid json.RawMessage would
// make the API's encoder abort mid-response, after the 200 was written.
func TestRandomValuesAreValidJSON(t *testing.T) {
	r := rand.New(rand.NewPCG(20260924, 1))
	for i := range 3000 {
		v := randomScVal(r, 4)
		out, err := Marshal(v)
		if err != nil {
			t.Fatalf("value %d (%s): %v", i, v.Type, err)
		}
		if !json.Valid(out) {
			t.Fatalf("value %d (%s): invalid JSON %s", i, v.Type, out)
		}
	}
}

func FuzzFromBase64(f *testing.F) {
	for _, v := range loadGolden(f) {
		f.Add(v.b64)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := FromBase64(s)
		if err == nil && !json.Valid(out) {
			t.Fatalf("%q rendered invalid JSON %s", s, out)
		}
	})
}

func nested(levels int) xdr.ScVal {
	v := xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	for range levels {
		vec := &xdr.ScVec{v}
		v = xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vec}
	}
	return v
}

func TestDepthLimit(t *testing.T) {
	if _, err := Marshal(nested(MaxDepth)); err != nil {
		t.Fatalf("depth %d must render: %v", MaxDepth, err)
	}
	if _, err := Marshal(nested(300)); !errors.Is(err, errDepth) {
		t.Fatalf("depth 300: err = %v, want the depth refusal", err)
	}
	// The XDR decoder accepts far deeper values than MaxDepth, so the limit
	// has to hold on the path from stored bytes too.
	b64, err := xdr.MarshalBase64(nested(300))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromBase64(b64); !errors.Is(err, errDepth) {
		t.Fatalf("depth 300 from base64: err = %v, want the depth refusal", err)
	}
}

// SDK accessors panic on a union whose arm pointer is nil; the encoder has
// to turn that into an error, never a crashed request.
func TestMalformedUnionRecovers(t *testing.T) {
	cases := map[string]xdr.ScVal{
		"i128 without parts":  {Type: xdr.ScValTypeScvI128},
		"vec without body":    {Type: xdr.ScValTypeScvVec},
		"string without body": {Type: xdr.ScValTypeScvString},
		"address without id": {Type: xdr.ScValTypeScvAddress, Address: &xdr.ScAddress{
			Type: xdr.ScAddressTypeScAddressTypeAccount}},
		"error without code": {Type: xdr.ScValTypeScvError, Error: &xdr.ScError{Type: xdr.ScErrorTypeSceAuth}},
		"wasm without hash": {Type: xdr.ScValTypeScvContractInstance, Instance: &xdr.ScContractInstance{
			Executable: xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableWasm}}},
		"unknown arm": {Type: xdr.ScValType(63)},
	}
	for name, v := range cases {
		if out, err := Marshal(v); err == nil {
			t.Errorf("%s: rendered %s, want an error", name, out)
		}
	}
}

// unescape undoes escape_bytes, the way a display layer must.
func unescape(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			out = append(out, s[i])
			continue
		}
		i++
		switch s[i] {
		case '0':
			out = append(out, 0)
		case 't':
			out = append(out, '\t')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 'x':
			n, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			out = append(out, byte(n))
			i += 2
		default:
			out = append(out, s[i])
		}
	}
	return out
}

// The escaping is lossless: every byte survives JSON parsing plus the
// reverse escape, for strings and symbols alike.
func TestStringEscapesRoundTrip(t *testing.T) {
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	inputs := [][]byte{all, []byte(`\x41`), []byte(`\\`)}
	for i := range all {
		inputs = append(inputs, all[i:i+1])
	}
	for _, raw := range inputs {
		str, sym := xdr.ScString(raw), xdr.ScSymbol(raw)
		for _, v := range []xdr.ScVal{
			{Type: xdr.ScValTypeScvString, Str: &str},
			{Type: xdr.ScValTypeScvSymbol, Sym: &sym},
		} {
			out, err := Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]string
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("%q: %v (%s)", raw, err, out)
			}
			for _, text := range parsed {
				if got := unescape(text); string(got) != string(raw) {
					t.Errorf("%q round-tripped to %q through %s", raw, got, out)
				}
			}
		}
	}
}

// BenchmarkLargestStoredValue renders the largest value in the golden file:
// a real escrow state entry of about 8.5 KB.
func BenchmarkLargestStoredValue(b *testing.B) {
	var largest vector
	for _, v := range loadGolden(b) {
		if len(v.b64) > len(largest.b64) {
			largest = v
		}
	}
	var v xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(largest.b64, &v); err != nil {
		b.Fatal(err)
	}
	b.Run("Marshal", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(largest.b64)))
		for b.Loop() {
			if _, err := Marshal(v); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("FromBase64", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(largest.b64)))
		for b.Loop() {
			if _, err := FromBase64(largest.b64); err != nil {
				b.Fatal(err)
			}
		}
	})
}
