package builder

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/types/builder/mevpb"
)

// maxGRPCAmountBytes bounds a wei amount carried as big-endian bytes: it ends up
// in a uint256, exactly like the JSON-RPC field.
const maxGRPCAmountBytes = 32

// ApplyGRPCFields copies the bsc-mev-validator extension of the gRPC
// BidBlockRequest (nontaxable_fee) into the args, so a BidBlock sent over gRPC
// means the same as one sent over mev_sendBidBlock.
//
// A request from a client built against the upstream proto does not carry the
// field: NontaxableFee is then left nil, which is what the gRPC entry point
// passed before the field existed.
func (b *BidBlockArgs) ApplyGRPCFields(req *mevpb.BidBlockRequest) error {
	if req == nil {
		return nil
	}
	raw := req.GetNontaxableFee()
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxGRPCAmountBytes {
		return NewInvalidBidError("nontaxable fee is longer than 32 bytes")
	}
	b.NontaxableFee = new(big.Int).SetBytes(raw)
	return nil
}
