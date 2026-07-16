// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package builder

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestParseNontaxableFee(t *testing.T) {
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	overflow := new(big.Int).Lsh(big.NewInt(1), 256)
	tests := []struct {
		name    string
		fee     *big.Int
		want    *uint256.Int
		wantErr bool
	}{
		{name: "nil", want: uint256.NewInt(0)},
		{name: "zero", fee: big.NewInt(0), want: uint256.NewInt(0)},
		{name: "positive", fee: big.NewInt(42), want: uint256.NewInt(42)},
		{name: "max uint256", fee: maxUint256, want: uint256.MustFromBig(maxUint256)},
		{name: "negative", fee: big.NewInt(-1), wantErr: true},
		{name: "overflow", fee: overflow, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseNontaxableFee(test.fee)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseNontaxableFee error: got %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got.Cmp(test.want) != 0 {
				t.Fatalf("fee mismatch: got %s, want %s", got, test.want)
			}
		})
	}
}

func TestBidArgsToBidRejectsInvalidFeesAndValuedPayback(t *testing.T) {
	signer := types.LatestSignerForChainID(big.NewInt(1))
	base := func() *BidArgs {
		return &BidArgs{RawBid: &RawBid{GasFee: big.NewInt(1)}}
	}

	negativeNontaxable := base()
	negativeNontaxable.NontaxableFee = big.NewInt(-1)
	if _, err := negativeNontaxable.ToBid(common.Address{}, signer); err == nil {
		t.Fatal("negative nontaxable fee must be rejected")
	}
	validNontaxable := base()
	validNontaxable.NontaxableFee = big.NewInt(42)
	validBid, err := validNontaxable.ToBid(common.Address{}, signer)
	if err != nil {
		t.Fatalf("positive nontaxable fee rejected: %v", err)
	}
	if validBid.NontaxableFee.Cmp(uint256.NewInt(42)) != 0 {
		t.Fatalf("decoded nontaxable fee: got %s, want 42", validBid.NontaxableFee)
	}

	payback := types.NewTransaction(0, common.Address{0x1}, big.NewInt(1), 21_000, big.NewInt(1), nil)
	paybackBytes, err := payback.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal payback: %v", err)
	}
	valuedPayback := base()
	valuedPayback.PayBidTx = paybackBytes
	if _, err := valuedPayback.ToBid(common.Address{}, signer); err == nil {
		t.Fatal("payback transaction with value must be rejected")
	}
}

func TestBidBlockArgsToDecodedBidBlockNormalizesNilSidecars(t *testing.T) {
	args := &BidBlockArgs{
		BidBlock: &BidBlock{
			Header: &types.Header{
				Difficulty: big.NewInt(1),
				Number:     big.NewInt(1),
				Extra:      make([]byte, 32),
			},
		},
	}

	decoded, err := args.ToDecodedBidBlock(common.Address{0x1})
	if err != nil {
		t.Fatalf("ToDecodedBidBlock failed: %v", err)
	}
	if decoded.Sidecars == nil {
		t.Fatal("nil sidecars should be normalized to an empty slice")
	}
	if len(decoded.Sidecars) != 0 {
		t.Fatalf("sidecars length mismatch: got %d, want 0", len(decoded.Sidecars))
	}
}

func TestBidBlockArgsToDecodedBidBlockCopiesHeader(t *testing.T) {
	args := &BidBlockArgs{
		BidBlock: &BidBlock{
			Header: &types.Header{
				Difficulty: big.NewInt(1),
				Number:     big.NewInt(1),
				Extra:      []byte{1, 2, 3},
			},
		},
	}

	decoded, err := args.ToDecodedBidBlock(common.Address{0x1})
	if err != nil {
		t.Fatalf("ToDecodedBidBlock failed: %v", err)
	}
	if decoded.Header == args.BidBlock.Header {
		t.Fatal("decoded BidBlock header must not share the original header pointer")
	}

	decoded.Header.Number.SetUint64(2)
	decoded.Header.Extra[0] = 9

	if args.BidBlock.Header.Number.Uint64() != 1 {
		t.Fatalf("original header number mutated: got %d, want 1", args.BidBlock.Header.Number.Uint64())
	}
	if args.BidBlock.Header.Extra[0] != 1 {
		t.Fatalf("original header extra mutated: got %d, want 1", args.BidBlock.Header.Extra[0])
	}
}

func TestBidBlockArgsToDecodedBidBlockNontaxableFee(t *testing.T) {
	fee := big.NewInt(123)
	args := &BidBlockArgs{
		BidBlock: &BidBlock{
			Header: &types.Header{Difficulty: big.NewInt(1), Number: big.NewInt(1)},
		},
		NontaxableFee: fee,
	}

	decoded, err := args.ToDecodedBidBlock(common.Address{0x1})
	if err != nil {
		t.Fatalf("ToDecodedBidBlock failed: %v", err)
	}
	fee.SetUint64(456)
	if decoded.NontaxableFee.Cmp(uint256.NewInt(123)) != 0 {
		t.Fatalf("decoded fee changed with wire value: got %s, want 123", decoded.NontaxableFee)
	}

	args.NontaxableFee = big.NewInt(-1)
	if _, err := args.ToDecodedBidBlock(common.Address{0x1}); err == nil {
		t.Fatal("negative BidBlock nontaxable fee must be rejected")
	}
	args.NontaxableFee = new(big.Int).Lsh(big.NewInt(1), 256)
	if _, err := args.ToDecodedBidBlock(common.Address{0x1}); err == nil {
		t.Fatal("overflowing BidBlock nontaxable fee must be rejected")
	}
}

func TestBidBlockArgsNontaxableFeeJSONRoundTripPreservesSignature(t *testing.T) {
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatalf("parse builder key: %v", err)
	}
	block := &BidBlock{
		Header: &types.Header{
			Difficulty: big.NewInt(1),
			Number:     big.NewInt(1),
			GasLimit:   30_000_000,
			GasUsed:    21_000,
		},
	}
	signature, err := crypto.Sign(block.Hash().Bytes(), key)
	if err != nil {
		t.Fatalf("sign BidBlock: %v", err)
	}
	args := &BidBlockArgs{
		BidBlock:      block,
		Signature:     signature,
		NontaxableFee: big.NewInt(123),
	}

	wire, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal BidBlockArgs: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatalf("inspect BidBlockArgs JSON: %v", err)
	}
	if _, ok := fields["nontaxableFee"]; !ok {
		t.Fatalf("BidBlockArgs JSON is missing nontaxableFee: %s", wire)
	}

	var decoded BidBlockArgs
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal BidBlockArgs: %v", err)
	}
	if decoded.NontaxableFee == nil || decoded.NontaxableFee.Cmp(big.NewInt(123)) != 0 {
		t.Fatalf("nontaxableFee did not round-trip: got %v, want 123", decoded.NontaxableFee)
	}
	wantBuilder := crypto.PubkeyToAddress(key.PublicKey)
	gotBuilder, err := decoded.EcrecoverSender()
	if err != nil {
		t.Fatalf("recover builder after JSON round-trip: %v", err)
	}
	if gotBuilder != wantBuilder {
		t.Fatalf("recovered builder mismatch: got %s, want %s", gotBuilder, wantBuilder)
	}

	// NontaxableFee is intentionally an unsigned lower-bound hint. Mutating it
	// must neither change the signed BidBlock hash nor the recovered builder.
	decoded.NontaxableFee.SetUint64(999)
	gotBuilder, err = decoded.EcrecoverSender()
	if err != nil {
		t.Fatalf("recover builder after fee mutation: %v", err)
	}
	if gotBuilder != wantBuilder {
		t.Fatalf("fee mutation changed recovered builder: got %s, want %s", gotBuilder, wantBuilder)
	}
}
