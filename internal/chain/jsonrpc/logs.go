package jsonrpc

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// filterLogs returns the logs matching q in blocks [from, to], asking for at most
// logsRange blocks per request.
func (s *Source) filterLogs(ctx context.Context, q ethereum.FilterQuery, from, to uint64) ([]types.Log, error) {
	var out []types.Log
	for start := from; start <= to; {
		end := min(to, start+s.logsRange-1)
		q.FromBlock = new(big.Int).SetUint64(start)
		q.ToBlock = new(big.Int).SetUint64(end)

		var logs []types.Log
		err := s.retry(ctx, func(ctx context.Context) (err error) {
			logs, err = s.eth.FilterLogs(ctx, q)
			return err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, logs...)

		if end == to {
			break
		}
		start = end + 1
	}

	return out, nil
}
