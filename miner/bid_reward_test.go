// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package miner

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	buildertypes "github.com/ethereum/go-ethereum/core/types/builder"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

type testCodeSizeReader map[common.Address]int

func (r testCodeSizeReader) GetCodeSize(address common.Address) int {
	return r[address]
}

func TestTotalBidReward(t *testing.T) {
	tests := []struct {
		gasFee     int64
		nontaxable uint64
		wantReward int64
	}{
		{gasFee: 100, wantReward: 90},
		{gasFee: 101, wantReward: 90},
		{gasFee: 90, nontaxable: 10, wantReward: 91},
	}
	for _, test := range tests {
		got := totalBidReward(big.NewInt(test.gasFee), uint256.NewInt(test.nontaxable))
		if got.Cmp(big.NewInt(test.wantReward)) != 0 {
			t.Fatalf("reward(%d, %d): got %s, want %d", test.gasFee, test.nontaxable, got, test.wantReward)
		}
	}

	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	got := totalBidReward(maxUint256, uint256.MustFromBig(maxUint256))
	want := new(big.Int).Add(calcRewardAfterBEP95(maxUint256), maxUint256)
	if got.Cmp(want) != 0 {
		t.Fatalf("large reward wrapped: got %s, want %s", got, want)
	}
}

func TestRewardStrictlyBetter(t *testing.T) {
	tests := []struct {
		name                 string
		candidate, incumbent *big.Int
		want                 bool
	}{
		{name: "nil candidate", incumbent: big.NewInt(1)},
		{name: "first candidate", candidate: big.NewInt(1), want: true},
		{name: "greater", candidate: big.NewInt(2), incumbent: big.NewInt(1), want: true},
		{name: "tie", candidate: big.NewInt(1), incumbent: big.NewInt(1)},
		{name: "lower", candidate: big.NewInt(1), incumbent: big.NewInt(2)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := rewardStrictlyBetter(test.candidate, test.incumbent); got != test.want {
				t.Fatalf("rewardStrictlyBetter: got %v, want %v", got, test.want)
			}
		})
	}
}

func TestCalcNontaxableFeeFiltersTransfers(t *testing.T) {
	receiver := common.Address{0x48}
	other := common.Address{0x99}
	makeTx := func(to *common.Address, gas uint64, value int64, data []byte) *types.Transaction {
		if to == nil {
			return types.NewContractCreation(0, big.NewInt(value), gas, big.NewInt(1), data)
		}
		return types.NewTransaction(0, *to, big.NewInt(value), gas, big.NewInt(1), data)
	}
	success := func() *types.Receipt { return &types.Receipt{Status: types.ReceiptStatusSuccessful} }
	failed := func() *types.Receipt { return &types.Receipt{Status: types.ReceiptStatusFailed} }

	tests := []struct {
		name    string
		tx      *types.Transaction
		receipt *types.Receipt
		want    uint64
	}{
		{name: "canonical transfer", tx: makeTx(&receiver, params.TxGas, 7, nil), receipt: success(), want: 7},
		{name: "failed transfer", tx: makeTx(&receiver, params.TxGas, 7, nil), receipt: failed()},
		{name: "wrong gas limit", tx: makeTx(&receiver, params.TxGas+1, 7, nil), receipt: success()},
		{name: "calldata", tx: makeTx(&receiver, params.TxGas, 7, []byte{0}), receipt: success()},
		{name: "wrong receiver", tx: makeTx(&other, params.TxGas, 7, nil), receipt: success()},
		{name: "contract creation", tx: makeTx(nil, params.TxGas, 7, nil), receipt: success()},
		{name: "zero value", tx: makeTx(&receiver, params.TxGas, 0, nil), receipt: success()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := calcNontaxableFee(
				[]common.Address{receiver},
				types.Transactions{test.tx},
				types.Receipts{test.receipt},
			)
			if err != nil {
				t.Fatalf("calcNontaxableFee: %v", err)
			}
			if got.Cmp(uint256.NewInt(test.want)) != 0 {
				t.Fatalf("fee mismatch: got %s, want %d", got, test.want)
			}
		})
	}

	transactions := types.Transactions{
		makeTx(&receiver, params.TxGas, 7, nil),
		makeTx(&receiver, params.TxGas, 11, nil),
	}
	got, err := calcNontaxableFee(
		[]common.Address{receiver, receiver},
		transactions,
		types.Receipts{success(), success()},
	)
	if err != nil {
		t.Fatalf("sum transfers: %v", err)
	}
	if got.Cmp(uint256.NewInt(18)) != 0 {
		t.Fatalf("duplicate receiver counted transfer twice: got %s, want 18", got)
	}
}

func TestCalcNontaxableFeeAdmissionAndReceiptModes(t *testing.T) {
	receiver := common.Address{0x48}
	tx := types.NewTransaction(0, receiver, big.NewInt(10), params.TxGas, big.NewInt(1), nil)
	txs := types.Transactions{tx}

	provisional, err := calcNontaxableFee([]common.Address{receiver}, txs, nil)
	if err != nil {
		t.Fatalf("admission calculation: %v", err)
	}
	if provisional.Cmp(uint256.NewInt(10)) != 0 {
		t.Fatalf("provisional fee: got %s, want 10", provisional)
	}

	// Receipt mode backs legacy bid simulation: failed transfers do not count.
	actual, err := calcNontaxableFee(
		[]common.Address{receiver},
		txs,
		types.Receipts{{Status: types.ReceiptStatusFailed}},
	)
	if err != nil {
		t.Fatalf("receipt-mode calculation: %v", err)
	}
	if !actual.IsZero() {
		t.Fatalf("failed transfer must not be counted: got %s", actual)
	}

	if _, err := calcNontaxableFee([]common.Address{receiver}, txs, types.Receipts{}); err == nil {
		t.Fatal("transaction/receipt length mismatch must fail")
	}

	if got, err := calcNontaxableFee(nil, txs, types.Receipts{{Status: types.ReceiptStatusSuccessful}}); err != nil || !got.IsZero() {
		t.Fatalf("empty receiver configuration: got %v, err %v", got, err)
	}
	if got, err := calcNontaxableFee([]common.Address{receiver}, types.Transactions{nil}, types.Receipts{nil}); err != nil || !got.IsZero() {
		t.Fatalf("nil transaction/receipt: got %v, err %v", got, err)
	}
}

func TestCalcNontaxableFeeRejectsSumOverflow(t *testing.T) {
	receiver := common.Address{0x48}
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	txs := types.Transactions{
		types.NewTransaction(0, receiver, max, params.TxGas, big.NewInt(1), nil),
		types.NewTransaction(1, receiver, max, params.TxGas, big.NewInt(1), nil),
	}
	if _, err := calcNontaxableFee(
		[]common.Address{receiver},
		txs,
		types.Receipts{{Status: types.ReceiptStatusSuccessful}, {Status: types.ReceiptStatusSuccessful}},
	); err == nil {
		t.Fatal("nontaxable transfer sum above uint256 must fail")
	}
}

func TestPrepareBidBlockNontaxableFeeExcludesSystemTxRegion(t *testing.T) {
	receiver := common.Address{0x48}
	userPayment := types.NewTransaction(0, receiver, big.NewInt(10), params.TxGas, big.NewInt(1), nil)
	// This structurally identical transfer is in the system-tx region and must
	// never enter the direct-payment score.
	systemTx := types.NewTransaction(1, receiver, big.NewInt(999), params.TxGas, big.NewInt(1), nil)
	decoded := &buildertypes.DecodedBidBlock{
		Txs:           types.Transactions{userPayment, systemTx},
		SystemTxStart: 1,
		NontaxableFee: uint256.NewInt(10),
	}
	if err := prepareBidBlockNontaxableFee(testCodeSizeReader{}, []common.Address{receiver}, decoded); err != nil {
		t.Fatalf("prepare BidBlock fee: %v", err)
	}
	if decoded.BidPriorityFee.Cmp(uint256.NewInt(10)) != 0 {
		t.Fatalf("provisional fee: got %s, want 10", decoded.BidPriorityFee)
	}

	// Declared NontaxableFee equal to the derived sum passed above; larger must fail.
	decoded.NontaxableFee = uint256.NewInt(11)
	if err := prepareBidBlockNontaxableFee(testCodeSizeReader{}, []common.Address{receiver}, decoded); err == nil {
		t.Fatal("claim larger than derived transfer value must fail admission")
	}
	decoded.NontaxableFee = uint256.NewInt(10)

	decoded.SystemTxStart = len(decoded.Txs) + 1
	if err := prepareBidBlockNontaxableFee(testCodeSizeReader{}, []common.Address{receiver}, decoded); err == nil {
		t.Fatal("out-of-range system tx start must fail instead of panicking")
	}
}

func TestPrepareBidBlockNontaxableFeeRequiresStableEOA(t *testing.T) {
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatalf("parse receiver key: %v", err)
	}
	receiver := crypto.PubkeyToAddress(key.PublicKey)
	payment := types.NewTransaction(0, receiver, big.NewInt(10), params.TxGas, big.NewInt(1), nil)
	decoded := &buildertypes.DecodedBidBlock{
		Txs:           types.Transactions{payment},
		SystemTxStart: 1,
		NontaxableFee: uint256.NewInt(10),
	}
	if err := prepareBidBlockNontaxableFee(testCodeSizeReader{receiver: 23}, []common.Address{receiver}, decoded); err == nil {
		t.Fatal("receiver with contract/delegation code must fail admission")
	}

	authorization, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(1),
		Address: common.Address{0x99},
	})
	if err != nil {
		t.Fatalf("sign set-code authorization: %v", err)
	}
	setCodeTx := types.NewTx(&types.SetCodeTx{
		ChainID:   uint256.NewInt(1),
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       100_000,
		To:        common.Address{0x1},
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{authorization},
	})
	decoded.Txs = types.Transactions{setCodeTx, payment}
	decoded.SystemTxStart = 2
	if err := prepareBidBlockNontaxableFee(testCodeSizeReader{}, []common.Address{receiver}, decoded); err == nil {
		t.Fatal("in-block EIP-7702 authorization for receiver must fail admission")
	}
}

func TestAddBidBlockUsesUnifiedReward(t *testing.T) {
	simulator := &bidSimulator{bestBidBlock: make(map[common.Hash]*buildertypes.DecodedBidBlock)}
	parent := common.Hash{0x1}
	first := &buildertypes.DecodedBidBlock{
		Header:         &types.Header{Number: big.NewInt(1)},
		GasFee:         big.NewInt(100),
		BidPriorityFee: uint256.NewInt(0),
	}
	second := &buildertypes.DecodedBidBlock{
		Header:         &types.Header{Number: big.NewInt(1)},
		GasFee:         big.NewInt(90),
		BidPriorityFee: uint256.NewInt(10),
	}
	if err := simulator.AddBidBlock(parent, first); err != nil {
		t.Fatalf("add first BidBlock: %v", err)
	}
	if err := simulator.AddBidBlock(parent, second); err != nil {
		t.Fatalf("lower gas fee but higher unified reward must win: %v", err)
	}
	if got := simulator.GetBestBidBlock(parent); got != second {
		t.Fatal("best BidBlock was not replaced by higher unified reward")
	}
	if err := simulator.AddBidBlock(parent, second); err == nil {
		t.Fatal("equal BidBlock reward must preserve the first incumbent")
	}
	lower := &buildertypes.DecodedBidBlock{
		Header:         &types.Header{Number: big.NewInt(1)},
		GasFee:         big.NewInt(1),
		BidPriorityFee: uint256.NewInt(0),
	}
	if err := simulator.AddBidBlock(common.Hash{0x2}, lower); err != nil {
		t.Fatalf("a different parent must have an independent best BidBlock: %v", err)
	}
}

func TestBidRuntimeUsesUnifiedRewardAndBothAssertions(t *testing.T) {
	bid := &buildertypes.Bid{GasFee: big.NewInt(100), NontaxableFee: uint256.NewInt(10)}
	runtime := newBidRuntime(bid)
	runtime.blockGasFeeByBid = uint256.NewInt(100)
	runtime.blockGasFeeTotal = uint256.NewInt(100)
	runtime.bidPriorityFee = uint256.NewInt(10)
	if !runtime.validReward() {
		t.Fatal("exact gas and EOA assertions must pass")
	}
	if got := runtime.blockReward(); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("legacy unified reward: got %s, want 100", got)
	}

	runtime.blockGasFeeByBid = uint256.NewInt(99)
	if runtime.validReward() {
		t.Fatal("gas fee below the builder assertion must fail")
	}
	runtime.blockGasFeeByBid = uint256.NewInt(100)
	runtime.bidPriorityFee = uint256.NewInt(9)
	if runtime.validReward() {
		t.Fatal("EOA contribution below the builder assertion must fail")
	}

	better := newBidRuntime(&buildertypes.Bid{GasFee: big.NewInt(100), NontaxableFee: uint256.NewInt(11)})
	if !better.isExpectedBetterThan(newBidRuntime(bid)) {
		t.Fatal("declared EOA contribution must participate in legacy bid preselection")
	}
}

func TestSelectBidBlockUsesUnifiedReward(t *testing.T) {
	w := new(worker)
	makeBidBlock := func(gasFee int64, nontaxable uint64) *buildertypes.DecodedBidBlock {
		return &buildertypes.DecodedBidBlock{
			Header:         &types.Header{Number: big.NewInt(1)},
			GasFee:         big.NewInt(gasFee),
			BidPriorityFee: uint256.NewInt(nontaxable),
		}
	}

	// 80 * 90% + 20 = 92, so BidBlock beats local=90 and legacy=91.
	if !w.selectBidBlock(makeBidBlock(80, 20), big.NewInt(91), big.NewInt(90)) {
		t.Fatal("BidBlock EOA contribution did not participate in three-way selection")
	}
	// Isolate the local gate: 100 * 90% = 90 ties local, with no legacy
	// candidate present to mask an accidental change in the local tie policy.
	if w.selectBidBlock(makeBidBlock(100, 0), nil, big.NewInt(90)) {
		t.Fatal("BidBlock tied with local reward must not replace local block")
	}
	// Isolate the legacy gate: 100 * 90% + 2 = 92 ties legacy, with no local
	// candidate present to mask an accidental change in the legacy tie policy.
	if w.selectBidBlock(makeBidBlock(100, 2), big.NewInt(92), nil) {
		t.Fatal("BidBlock tied with legacy reward must not replace legacy bid")
	}
}
