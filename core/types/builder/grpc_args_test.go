package builder

import (
	"bytes"
	"math/big"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/ethereum/go-ethereum/core/types/builder/mevpb"
)

// upstreamRequest encodes a BidBlockRequest the way a client built from the upstream proto does:
// only fields 1-3 exist.
func upstreamRequest(rlp, sig []byte, host string) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendBytes(b, rlp)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendBytes(b, sig)
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, host)
	return b
}

// An upstream client knows nothing about the field: the validator must see exactly what it saw
// before it existed.
func TestUpstreamClientRequestIsUnchanged(t *testing.T) {
	var req mevpb.BidBlockRequest
	if err := proto.Unmarshal(upstreamRequest([]byte{0xc0}, []byte{1, 2, 3}, "v.example"), &req); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.BidBlockRlp, []byte{0xc0}) || !bytes.Equal(req.Signature, []byte{1, 2, 3}) || req.ValidatorHostName != "v.example" {
		t.Fatalf("upstream fields changed: %+v", &req)
	}
	args := BidBlockArgs{Signature: req.Signature}
	if err := args.ApplyGRPCFields(&req); err != nil {
		t.Fatal(err)
	}
	if args.NontaxableFee != nil {
		t.Fatalf("an upstream request must leave NontaxableFee unset, got %v", args.NontaxableFee)
	}
	if err := args.ApplyGRPCFields(nil); err != nil {
		t.Fatal(err)
	}
}

// A server built from the upstream proto must be able to read a request that carries the field:
// fields 1-3 are byte-identical at the front and the extension is a separate, skippable field
// outside the upstream range (1-3 used, 4-10 reserved).
func TestExtendedRequestIsReadableByUpstreamServer(t *testing.T) {
	data, err := proto.Marshal(&mevpb.BidBlockRequest{
		BidBlockRlp:       []byte{0xc0},
		Signature:         []byte{1, 2, 3},
		ValidatorHostName: "v.example",
		NontaxableFee:     []byte{0x01},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[protowire.Number][]byte{}
	for rest := data; len(rest) > 0; {
		num, typ, n := protowire.ConsumeTag(rest)
		if n < 0 {
			t.Fatal("bad tag")
		}
		rest = rest[n:]
		m := protowire.ConsumeFieldValue(num, typ, rest)
		if m < 0 {
			t.Fatalf("field %d cannot be skipped by a generic parser", num)
		}
		seen[num] = rest[:m]
		rest = rest[m:]
	}
	if _, ok := seen[481]; !ok || len(seen) != 4 {
		t.Fatalf("expected fields 1, 2, 3 and 481 on the wire, got %d fields", len(seen))
	}
	var kept []byte
	for _, num := range []protowire.Number{1, 2, 3} {
		kept = protowire.AppendTag(kept, num, protowire.BytesType)
		kept = append(kept, seen[num]...)
	}
	if !bytes.Equal(kept, upstreamRequest([]byte{0xc0}, []byte{1, 2, 3}, "v.example")) {
		t.Fatal("fields 1-3 are not what an upstream client would have sent")
	}
}

// Over gRPC the field means what nontaxableFee means over mev_sendBidBlock.
func TestGRPCNontaxableFeeMapsToBidBlockArgs(t *testing.T) {
	fee, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	data, err := proto.Marshal(&mevpb.BidBlockRequest{NontaxableFee: fee.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	var got mevpb.BidBlockRequest
	if err := proto.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	var args BidBlockArgs
	if err := args.ApplyGRPCFields(&got); err != nil {
		t.Fatal(err)
	}
	if args.NontaxableFee.Cmp(fee) != 0 {
		t.Fatalf("got %v, want %v", args.NontaxableFee, fee)
	}
	// the amount passes the same parser the JSON-RPC path uses
	if parsed, err := parseNontaxableFee(args.NontaxableFee); err != nil || parsed.ToBig().Cmp(fee) != 0 {
		t.Fatalf("parseNontaxableFee: %v %v", parsed, err)
	}

	// the largest uint256 is accepted, one byte more is not
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	args = BidBlockArgs{}
	if err := args.ApplyGRPCFields(&mevpb.BidBlockRequest{NontaxableFee: max.Bytes()}); err != nil || args.NontaxableFee.Cmp(max) != 0 {
		t.Fatalf("max uint256: %v %v", args.NontaxableFee, err)
	}
	args = BidBlockArgs{}
	err = args.ApplyGRPCFields(&mevpb.BidBlockRequest{NontaxableFee: make([]byte, 33)})
	if err == nil || args.NontaxableFee != nil {
		t.Fatalf("33-byte amount: err=%v fee=%v", err, args.NontaxableFee)
	}
	if coded, ok := err.(interface{ ErrorCode() int }); !ok || coded.ErrorCode() != InvalidBidParamError {
		t.Fatalf("want an invalid-bid error, got %v", err)
	}
}
