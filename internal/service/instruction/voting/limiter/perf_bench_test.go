package limiter

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// BenchmarkTopPending measures the scrape-time cost of the top_provider_unfinalized_proposals
// collector's backing call: a full filter + sort of the per-voter pending map under RLock.
func BenchmarkTopPending(b *testing.B) {
	const voters = 200

	addrs := make([]common.Address, voters)
	for i := range addrs {
		addrs[i][18] = byte(i >> 8)
		addrs[i][19] = byte(i)
	}

	l := New(addrs, 1000)
	// Give every voter a non-zero pending count so all are candidates for the sort.
	for i, a := range addrs {
		for j := 0; j <= i%5; j++ {
			_ = l.Increment(a)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = l.TopPending(3)
	}
}
