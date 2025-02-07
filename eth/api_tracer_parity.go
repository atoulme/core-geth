// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/rpc"
)

// TraceFilterArgs represents the arguments for a call.
type TraceFilterArgs struct {
	FromBlock   hexutil.Uint64  `json:"fromBlock,omitempty"`   // Trace from this starting block
	ToBlock     hexutil.Uint64  `json:"toBlock,omitempty"`     // Trace utill this end block
	FromAddress *common.Address `json:"fromAddress,omitempty"` // Sent from these addresses
	ToAddress   *common.Address `json:"toAddress,omitempty"`   // Sent to these addresses
	After       uint64          `json:"after,omitempty"`       // The offset trace number
	Count       uint64          `json:"count,omitempty"`       // Integer number of traces to display in a batch
}

// ParityTrace A trace in the desired format (Parity/OpenEtherum) See: https://Parity.github.io/wiki/JSONRPC-trace-module
type ParityTrace struct {
	Action              TraceRewardAction `json:"action"`
	BlockHash           common.Hash       `json:"blockHash"`
	BlockNumber         uint64            `json:"blockNumber"`
	Error               string            `json:"error,omitempty"`
	Result              interface{}       `json:"result"`
	Subtraces           int               `json:"subtraces"`
	TraceAddress        []int             `json:"traceAddress"`
	TransactionHash     *common.Hash      `json:"transactionHash"`
	TransactionPosition *uint64           `json:"transactionPosition"`
	Type                string            `json:"type"`
}

// TraceRewardAction An Parity formatted trace reward action
type TraceRewardAction struct {
	Value      *hexutil.Big    `json:"value,omitempty"`
	Author     *common.Address `json:"author,omitempty"`
	RewardType string          `json:"rewardType,omitempty"`
}

// setTraceConfigDefaultTracer sets the default tracer to "callTracerParity" if none set
func setTraceConfigDefaultTracer(config *TraceConfig) *TraceConfig {
	if config == nil {
		config = &TraceConfig{}
	}

	if config.Tracer == nil {
		tracer := "callTracerParity"
		config.Tracer = &tracer
	}

	return config
}

// decorateResponse applies formatting to trace results if needed.
func decorateResponse(res interface{}, config *TraceConfig) (interface{}, error) {
	if config != nil && config.NestedTraceOutput && config.Tracer != nil {
		return decorateNestedTraceResponse(res, *config.Tracer), nil
	}
	return res, nil
}

// decorateNestedTraceResponse formats trace results the way Parity does.
// Docs: https://openethereum.github.io/JSONRPC-trace-module
// Example:
/*
{
  "id": 1,
  "jsonrpc": "2.0",
  "result": {
    "output": "0x",
    "stateDiff": { ... },
    "trace": [ { ... }, ],
    "vmTrace": { ... }
  }
}
*/
func decorateNestedTraceResponse(res interface{}, tracer string) interface{} {
	out := map[string]interface{}{}
	if tracer == "callTracerParity" {
		out["trace"] = res
	} else if tracer == "stateDiffTracer" {
		out["stateDiff"] = res
	} else {
		return res
	}
	return out
}

func traceBlockReward(ctx context.Context, eth *Ethereum, block *types.Block, config *TraceConfig) (*ParityTrace, error) {
	chainConfig := eth.blockchain.Config()
	minerReward, _ := ethash.GetRewards(chainConfig, block.Header(), block.Uncles())

	coinbase := block.Coinbase()

	tr := &ParityTrace{
		Type: "reward",
		Action: TraceRewardAction{
			Value:      (*hexutil.Big)(minerReward),
			Author:     &coinbase,
			RewardType: "block",
		},
		TraceAddress: []int{},
		BlockHash:    block.Hash(),
		BlockNumber:  block.NumberU64(),
	}

	return tr, nil
}

func traceBlockUncleRewards(ctx context.Context, eth *Ethereum, block *types.Block, config *TraceConfig) ([]*ParityTrace, error) {
	chainConfig := eth.blockchain.Config()
	_, uncleRewards := ethash.GetRewards(chainConfig, block.Header(), block.Uncles())

	results := make([]*ParityTrace, len(uncleRewards))
	for i, uncle := range block.Uncles() {
		if i < len(uncleRewards) {
			coinbase := uncle.Coinbase

			results[i] = &ParityTrace{
				Type: "reward",
				Action: TraceRewardAction{
					Value:      (*hexutil.Big)(uncleRewards[i]),
					Author:     &coinbase,
					RewardType: "uncle",
				},
				TraceAddress: []int{},
				BlockNumber:  block.NumberU64(),
				BlockHash:    block.Hash(),
			}
		}
	}

	return results, nil
}

// Block returns the structured logs created during the execution of
// EVM and returns them as a JSON object.
// The correct name will be TraceBlockByNumber, though we want to be compatible with Parity trace module.
func (api *PrivateTraceAPI) Block(ctx context.Context, number rpc.BlockNumber, config *TraceConfig) ([]interface{}, error) {
	// Fetch the block that we want to trace
	var block *types.Block

	switch number {
	case rpc.PendingBlockNumber:
		block = api.eth.miner.PendingBlock()
	case rpc.LatestBlockNumber:
		block = api.eth.blockchain.CurrentBlock()
	default:
		block = api.eth.blockchain.GetBlockByNumber(uint64(number))
	}
	// Trace the block if it was found
	if block == nil {
		return nil, fmt.Errorf("block #%d not found", number)
	}

	config = setTraceConfigDefaultTracer(config)

	traceResults, err := traceBlockByNumber(ctx, api.eth, number, config)
	if err != nil {
		return nil, err
	}

	traceReward, err := traceBlockReward(ctx, api.eth, block, config)
	if err != nil {
		return nil, err
	}

	traceUncleRewards, err := traceBlockUncleRewards(ctx, api.eth, block, config)
	if err != nil {
		return nil, err
	}

	results := []interface{}{}

	for _, result := range traceResults {
		var tmp []interface{}
		if err := json.Unmarshal(result.Result.(json.RawMessage), &tmp); err != nil {
			return nil, err
		}
		results = append(results, tmp...)
	}

	results = append(results, traceReward)

	for _, uncleReward := range traceUncleRewards {
		results = append(results, uncleReward)
	}

	return results, nil
}

// Transaction returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (api *PrivateTraceAPI) Transaction(ctx context.Context, hash common.Hash, config *TraceConfig) (interface{}, error) {
	config = setTraceConfigDefaultTracer(config)
	return traceTransaction(ctx, api.eth, hash, config)
}

// Filter configures a new tracer according to the provided configuration, and
// executes all the transactions contained within. The return value will be one item
// per transaction, dependent on the requested tracer.
func (api *PrivateTraceAPI) Filter(ctx context.Context, args TraceFilterArgs, config *TraceConfig) (*rpc.Subscription, error) {
	config = setTraceConfigDefaultTracer(config)

	// Fetch the block interval that we want to trace
	start := uint64(args.FromBlock)
	end := uint64(args.ToBlock)

	from := api.eth.blockchain.GetBlockByNumber(start)
	to := api.eth.blockchain.GetBlockByNumber(end)

	// Trace the chain if we've found all our blocks
	if from == nil {
		return nil, fmt.Errorf("starting block #%d not found", start)
	}
	if to == nil {
		return nil, fmt.Errorf("end block #%d not found", end)
	}
	if from.Number().Cmp(to.Number()) >= 0 {
		return nil, fmt.Errorf("end block (#%d) needs to come after start block (#%d)", end, start)
	}
	return traceChain(ctx, api.eth, from, to, config)
}

// Call lets you trace a given eth_call. It collects the structured logs created during the execution of EVM
// if the given transaction was added on top of the provided block and returns them as a JSON object.
// You can provide -2 as a block number to trace on top of the pending block.
func (api *PrivateTraceAPI) Call(ctx context.Context, args ethapi.CallArgs, blockNrOrHash rpc.BlockNumberOrHash, config *TraceConfig) (interface{}, error) {
	config = setTraceConfigDefaultTracer(config)
	res, err := traceCall(ctx, api.eth, args, blockNrOrHash, config)
	if err != nil {
		return nil, err
	}
	return decorateResponse(res, config)
}

// CallMany lets you trace a given eth_call. It collects the structured logs created during the execution of EVM
// if the given transaction was added on top of the provided block and returns them as a JSON object.
// You can provide -2 as a block number to trace on top of the pending block.
func (api *PrivateTraceAPI) CallMany(ctx context.Context, txs []ethapi.CallArgs, blockNrOrHash rpc.BlockNumberOrHash, config *TraceConfig) (interface{}, error) {
	config = setTraceConfigDefaultTracer(config)
	res, err := traceCallMany(ctx, api.eth, txs, blockNrOrHash, config)
	if err != nil {
		return nil, err
	}
	return res, nil
}
