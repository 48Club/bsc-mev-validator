// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package miner

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	buildertypes "github.com/ethereum/go-ethereum/core/types/builder"
	"github.com/ethereum/go-ethereum/params"
)

const (
	// The 48Club reward policy values gas fees after the current 10% BEP-95 burn.
	bep95RewardNumerator   = 90
	bep95RewardDenominator = 100
)

// calcRewardAfterBEP95 returns the validator/delegator reward represented by a
// pre-BEP-95 gas fee. big.Int is used because bid inputs are untrusted and
// intermediate uint256 multiplication/addition must not wrap.
func calcRewardAfterBEP95(gasFee *big.Int) *big.Int {
	if gasFee == nil || gasFee.Sign() <= 0 {
		return new(big.Int)
	}
	reward := new(big.Int).Mul(new(big.Int).Set(gasFee), big.NewInt(bep95RewardNumerator))
	return reward.Div(reward, big.NewInt(bep95RewardDenominator))
}

func totalBidReward(gasFee *big.Int, nontaxableFee *uint256.Int) *big.Int {
	reward := calcRewardAfterBEP95(gasFee)
	if nontaxableFee != nil {
		reward.Add(reward, nontaxableFee.ToBig())
	}
	return reward
}

// rewardStrictlyBetter implements the common tie policy used for local,
// legacy-bid and BidBlock selection. An equal remote reward never replaces the
// incumbent candidate.
func rewardStrictlyBetter(candidate, incumbent *big.Int) bool {
	if candidate == nil {
		return false
	}
	return incumbent == nil || candidate.Cmp(incumbent) > 0
}

type accountCodeSizeReader interface {
	GetCodeSize(common.Address) int
}

// validateBidBlockReceiverEOAs checks the state premise that makes a 21,000
// gas native transfer non-reverting without executing the BidBlock. Existing
// code includes EIP-7702 delegation designators. A block-local authorization
// targeting a configured receiver is rejected conservatively because it could
// install code before a later payment.
func validateBidBlockReceiverEOAs(parentState accountCodeSizeReader, receiverEOAs []common.Address, txs types.Transactions) error {
	if len(receiverEOAs) == 0 {
		return nil
	}
	if parentState == nil {
		return errors.New("parent state is required to validate BidBlock receiver EOAs")
	}
	receivers := make(map[common.Address]struct{}, len(receiverEOAs))
	for _, receiver := range receiverEOAs {
		receivers[receiver] = struct{}{}
		if codeSize := parentState.GetCodeSize(receiver); codeSize != 0 {
			return fmt.Errorf("configured nontaxable receiver %s is not an EOA: code size %d", receiver, codeSize)
		}
	}
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		for _, authority := range tx.SetCodeAuthorities() {
			if _, ok := receivers[authority]; ok {
				return fmt.Errorf("BidBlock contains an EIP-7702 authorization for nontaxable receiver %s", authority)
			}
		}
	}
	return nil
}

// prepareBidBlockNontaxableFee performs the complete pre-seal direct-payment
// admission step and stores the transaction-derived value used for ranking.
func prepareBidBlockNontaxableFee(parentState accountCodeSizeReader, receiverEOAs []common.Address, decoded *buildertypes.DecodedBidBlock) error {
	if decoded == nil {
		return errors.New("nil decoded BidBlock")
	}
	if decoded.SystemTxStart < 0 || decoded.SystemTxStart > len(decoded.Txs) {
		return fmt.Errorf("invalid BidBlock system tx start: %d for %d transactions", decoded.SystemTxStart, len(decoded.Txs))
	}
	userTxs := decoded.Txs[:decoded.SystemTxStart]
	if err := validateBidBlockReceiverEOAs(parentState, receiverEOAs, userTxs); err != nil {
		return err
	}
	actual, err := calcNontaxableFee(receiverEOAs, userTxs, nil)
	if err != nil {
		return err
	}
	decoded.BidPriorityFee = actual
	if decoded.NontaxableFee != nil && actual.Cmp(decoded.NontaxableFee) < 0 {
		return fmt.Errorf("nontaxable fee does not achieve the expectation: got %s, want at least %s", actual, decoded.NontaxableFee)
	}
	return nil
}

// calcNontaxableFee sums canonical direct transfers to validator-owned EOAs.
// A nil receipts slice performs the structural BidBlock admission check, which
// is exact for any valid imported block: a 21,000-gas transfer to a code-free
// EOA cannot fail, and an underfunded sender invalidates the whole block. A
// non-nil slice additionally requires a successful receipt for every counted
// transfer and is used by legacy bid simulation.
func calcNontaxableFee(receiverEOAs []common.Address, txs types.Transactions, receipts types.Receipts) (*uint256.Int, error) {
	if receipts != nil && len(receipts) != len(txs) {
		return nil, fmt.Errorf("transaction/receipt count mismatch: txs %d, receipts %d", len(txs), len(receipts))
	}
	if len(receiverEOAs) == 0 || len(txs) == 0 {
		return uint256.NewInt(0), nil
	}

	receivers := make(map[common.Address]struct{}, len(receiverEOAs))
	for _, receiver := range receiverEOAs {
		receivers[receiver] = struct{}{}
	}
	total := new(big.Int)
	for i, tx := range txs {
		if tx == nil || tx.Gas() != params.TxGas || len(tx.Data()) != 0 || tx.Value().Sign() <= 0 {
			continue
		}
		to := tx.To()
		if to == nil {
			continue
		}
		if _, ok := receivers[*to]; !ok {
			continue
		}
		if receipts != nil && (receipts[i] == nil || receipts[i].Status != types.ReceiptStatusSuccessful) {
			continue
		}
		total.Add(total, tx.Value())
	}
	if total.BitLen() > 256 {
		return nil, fmt.Errorf("nontaxable fee sum overflows uint256")
	}
	return uint256.MustFromBig(total), nil
}
