package payload

import (
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/util"
)

// The node can broadcast the object information it owns by this message.
// The message can be sent automatically or can be used to answer getblock messages.

// InventoryType is the type of an object in the Inventory message.
type InventoryType uint8

// String implements the Stringer interface.
func (i InventoryType) String() string {
	switch i {
	case TXType:
		return "TX"
	case BlockType:
		return "block"
	case ConsensusType:
		return "consensus"
	default:
		return "unknown inventory type"
	}
}

// Valid returns true if the inventory (type) is known.
func (i InventoryType) Valid() bool {
	return i == BlockType || i == TXType || i == ConsensusType
}

// List of valid InventoryTypes.
const (
	TXType        InventoryType = 0x2b
	BlockType     InventoryType = 0x2c
	ConsensusType InventoryType = 0x2d
)

// Inventory payload.
type Inventory struct {
	// Type if the object hash.
	Type InventoryType

	// A list of hashes.
	Hashes []util.Uint256
}

// NewInventory return a pointer to an Inventory.
func NewInventory(typ InventoryType, hashes []util.Uint256) *Inventory {
	return &Inventory{
		Type:   typ,
		Hashes: hashes,
	}
}

// DecodeBinary implements Serializable interface.
func (p *Inventory) DecodeBinary(br *io.BinReader) {
	p.Type = InventoryType(br.ReadB())
	br.ReadArray(&p.Hashes)
}

// EncodeBinary implements Serializable interface.
func (p *Inventory) EncodeBinary(bw *io.BinWriter) {
	bw.WriteB(byte(p.Type))
	bw.WriteArray(p.Hashes)
}
