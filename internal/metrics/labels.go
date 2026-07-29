package metrics

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/op"
)

// knownOPCommands bounds the op_command label: the operation hash decodes
// client-influenced bytes, so anything outside this set collapses to "other"
// to keep label cardinality bounded.
//
// Keep this set in sync with the op.Command constants in go-flare-common's tee/op
// package. A command added upstream but missing here is reported as "other" (never an
// error), so drift degrades label granularity rather than breaking collection.
var knownOPCommands = map[op.Command]bool{
	op.TEEAttestation:             true,
	op.KeyDataProviderRestore:     true,
	op.KeyDataProviderRestoreTest: true,
	op.KeyDelete:                  true,
	op.KeyDirectBackup:            true,
	op.KeyDirectRestore:           true,
	op.KeyGenerate:                true,
	op.VRF:                        true,
	op.KeyInfo:                    true,
	op.KeyProof:                   true,
	op.TEEBackup:                  true,
	op.TEEInfo:                    true,
	op.InitializePolicy:           true,
	op.UpdatePolicy:               true,
	op.SetMachinePathList:         true,
	op.Pay:                        true,
	op.Reissue:                    true,
	op.Prove:                      true,
}

// opCommandLabel returns the operation command name when recognized, else "other".
func opCommandLabel(h common.Hash) string {
	c := op.HashToOPCommand(h)
	if knownOPCommands[c] {
		return string(c)
	}
	return "other"
}
