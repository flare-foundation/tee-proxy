package chain

import (
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/stretchr/testify/require"
)

const testABI = `[
	{"type":"event","name":"Done","inputs":[]},
	{"type":"function","name":"run","inputs":[],"outputs":[]},
	{"type":"error","name":"Failed","inputs":[]}
]`

func TestMust(t *testing.T) {
	a := MustABI(&bind.MetaData{ABI: testABI})

	require.Equal(t, "Done", MustEvent(a, "Done").Name)
	require.Equal(t, "run", MustMethod(a, "run").Name)
	require.Equal(t, "Failed", MustError(a, "Failed").Name)

	tests := []struct {
		name string
		f    func()
	}{
		{"abi", func() { MustABI(&bind.MetaData{ABI: "bad"}) }},
		{"event", func() { MustEvent(a, "run") }},
		{"method", func() { MustMethod(a, "Done") }},
		{"error", func() { MustError(a, "Done") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Panics(t, tc.f)
		})
	}
}
