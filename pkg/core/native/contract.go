package native

import (
	"github.com/nspcc-dev/neo-go/pkg/core/interop"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/util"
	"github.com/nspcc-dev/neo-go/pkg/vm/emit"
	"github.com/nspcc-dev/neo-go/pkg/vm/opcode"
)

// Contracts is a set of registered native contracts.
type Contracts struct {
	NEO       *NEO
	GAS       *GAS
	Policy    *Policy
	Contracts []interop.Contract
	// persistScript is vm script which executes "onPersist" method of every native contract.
	persistScript []byte
}

// ByHash returns native contract with the specified hash.
func (cs *Contracts) ByHash(h util.Uint160) interop.Contract {
	for _, ctr := range cs.Contracts {
		if ctr.Metadata().Hash.Equals(h) {
			return ctr
		}
	}
	return nil
}

// NewContracts returns new set of native contracts with new GAS, NEO and Policy
// contracts.
func NewContracts() *Contracts {
	cs := new(Contracts)

	gas := NewGAS()
	neo := NewNEO()
	neo.GAS = gas
	gas.NEO = neo

	cs.GAS = gas
	cs.Contracts = append(cs.Contracts, gas)
	cs.NEO = neo
	cs.Contracts = append(cs.Contracts, neo)

	policy := newPolicy()
	cs.Policy = policy
	cs.Contracts = append(cs.Contracts, policy)
	return cs
}

// GetPersistScript returns VM script calling "onPersist" method of every native contract.
func (cs *Contracts) GetPersistScript() []byte {
	if cs.persistScript != nil {
		return cs.persistScript
	}
	w := io.NewBufBinWriter()
	for i := range cs.Contracts {
		md := cs.Contracts[i].Metadata()
		emit.Int(w.BinWriter, 0)
		emit.Opcode(w.BinWriter, opcode.NEWARRAY)
		emit.String(w.BinWriter, "onPersist")
		emit.AppCall(w.BinWriter, md.Hash)
		emit.Opcode(w.BinWriter, opcode.DROP)
	}
	cs.persistScript = w.Bytes()
	return cs.persistScript
}
