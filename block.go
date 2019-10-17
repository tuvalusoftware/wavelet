package wavelet

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
	"io"
)

type Block struct {
	Index        uint64
	Merkle       MerkleNodeID
	Transactions []TransactionID

	ID BlockID
}

func NewBlock(index uint64, merkle MerkleNodeID, ids ...TransactionID) Block {
	b := Block{Index: index, Merkle: merkle, Transactions: ids}
	b.ID = blake2b.Sum256(b.Marshal())

	return b
}

func (b *Block) GetID() string {
	if b == nil || b.ID == ZeroBlockID {
		return ""
	}

	return fmt.Sprintf("%x", b.ID)
}

func (b Block) Marshal() []byte {
	buf := make([]byte, 8+SizeMerkleNodeID+4+len(b.Transactions)*SizeTransactionID)
	binary.BigEndian.PutUint64(buf[:8], b.Index)

	copy(buf[8:8+SizeMerkleNodeID], b.Merkle[:])

	binary.BigEndian.PutUint32(buf[8+SizeMerkleNodeID:8+SizeMerkleNodeID+4], uint32(len(b.Transactions)))

	for i, id := range b.Transactions {
		copy(buf[8+SizeMerkleNodeID+4+SizeTransactionID*i:8+4+SizeTransactionID*(i+1)], id[:])
	}

	return buf
}

func (b Block) String() string {
	return hex.EncodeToString(b.ID[:])
}

func UnmarshalBlock(r io.Reader) (block Block, err error) {
	var buf [8]byte

	if _, err = io.ReadFull(r, buf[:]); err != nil {
		err = errors.Wrap(err, "failed to decode block index")
		return
	}
	block.Index = binary.BigEndian.Uint64(buf[:8])

	if _, err = io.ReadFull(r, block.Merkle[:]); err != nil {
		err = errors.Wrap(err, "failed to decode block's merkle root")
		return
	}

	if _, err = io.ReadFull(r, buf[:4]); err != nil {
		err = errors.Wrap(err, "failed to decode block's transactions length")
		return
	}

	block.Transactions = make([]TransactionID, binary.BigEndian.Uint32(buf[:4]))

	for i := 0; i < len(block.Transactions); i++ {
		if _, err = io.ReadFull(r, block.Transactions[i][:]); err != nil {
			err = errors.Wrap(err, "failed to decode one of the transactions")
			return
		}
	}

	block.ID = blake2b.Sum256(block.Marshal())

	return
}
