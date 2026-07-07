package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
)

func benchConfigAll() Config {
	return Config{
		Enable: true, HTTP: true, Storage: true, Queue: true, Voting: true,
		ActiveVoters: true, Result: true, Info: true, Attestation: true,
		Policy: true, Liveness: true, Node: true, Runtime: true,
	}
}

// Per-call cost of the hottest update methods, enabled vs the opt-out paths.

func BenchmarkObserveHTTPEnabled(b *testing.B) {
	m := New(Config{Enable: true, HTTP: true})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ObserveHTTP("internal", "GET /healthy", 200, 5*time.Millisecond)
	}
}

func BenchmarkObserveHTTPNil(b *testing.B) {
	var m *Metrics
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ObserveHTTP("internal", "GET /healthy", 200, 5*time.Millisecond)
	}
}

func BenchmarkObserveHTTPGroupOff(b *testing.B) {
	m := New(Config{Enable: true, HTTP: false})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ObserveHTTP("internal", "GET /healthy", 200, 5*time.Millisecond)
	}
}

func BenchmarkObserveStorageEnabled(b *testing.B) {
	m := New(Config{Enable: true, Storage: true})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Observe("redis", "results", "get", "success", time.Millisecond)
	}
}

func BenchmarkObserveStorageNil(b *testing.B) {
	var m *Metrics
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Observe("redis", "results", "get", "success", time.Millisecond)
	}
}

func BenchmarkObserveNodeWaitEnabled(b *testing.B) {
	m := New(Config{Enable: true, Node: true})
	err := errors.New("x")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ObserveNodeWait("info", 10*time.Millisecond, err)
	}
}

func BenchmarkResultProcessedEnabled(b *testing.B) {
	m := New(Config{Enable: true, Result: true})
	h := op.KeyGenerate.Hash()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ResultProcessed(h, 1) // includes the opCommandLabel map lookup
	}
}

func BenchmarkInstructionRejectedEnabled(b *testing.B) {
	m := New(Config{Enable: true, Voting: true})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.InstructionRejected("wrong_tee_id")
	}
}

// Scrape cost: one /metrics Gather over the full registry, with the scrape-time
// gauges registered. Runtime collectors (go_*/process_*) dominate, so measure with
// and without them.

func registerScrapeGauges(m *Metrics) {
	m.RegisterQueueDepth("main", func() float64 { return 7 })
	m.RegisterQueueDepth("direct", func() float64 { return 0 })
	m.RegisterQueueDepth("backup", func() float64 { return 3 })
	m.RegisterActiveDataProviderVoters(func() float64 { return 42 })
	m.RegisterActiveInitiators(func() float64 { return 10 })
	m.RegisterOldestStoredPolicy(func() float64 { return 100 })
	m.RegisterInfoDelay(func() float64 { return 1.5 })
	top := []ProviderPending{
		{Provider: "0x000000000000000000000000000000000000000a", Pending: 3},
		{Provider: "0x000000000000000000000000000000000000000b", Pending: 2},
		{Provider: "0x000000000000000000000000000000000000000c", Pending: 1},
	}
	m.RegisterTopUnfinalizedProposals(func() []ProviderPending { return top })
	// Seed a few label series so Gather has realistic domain work.
	for range 8 {
		m.ObserveHTTP("internal", "GET /healthy", 200, time.Millisecond)
		m.Observe("redis", "results", "get", "success", time.Millisecond)
		m.ObserveNodeWait("info", 10*time.Millisecond, nil)
	}
}

func BenchmarkGatherAllGroups(b *testing.B) {
	m := New(benchConfigAll())
	registerScrapeGauges(m)
	reg := m.Registry()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Gather(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGatherDomainOnly(b *testing.B) {
	cfg := benchConfigAll()
	cfg.Runtime = false // exclude go_*/process_* collectors
	m := New(cfg)
	registerScrapeGauges(m)
	reg := m.Registry()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Gather(); err != nil {
			b.Fatal(err)
		}
	}
}
