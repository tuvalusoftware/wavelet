package wctl

import (
	"encoding/binary"
	"encoding/hex"
	"errors"

	"github.com/perlin-network/wavelet"
	"github.com/perlin-network/wavelet/sys"
)

var ErrNotContract = errors.New("address is not smart contract")

// Call calls a smart contract function
func (c *Client) Call(recipient [32]byte, fn FunctionCall) (*TxResponse, error) {
	a, err := c.GetSelf()
	if err != nil {
		return nil, err
	}

	if !c.RecipientIsContract(recipient) {
		return nil, ErrNotContract
	}

	if a.Balance < fn.Amount+fn.GasLimit {
		return nil, ErrInsufficientPerls
	}

	return c.sendTransfer(byte(sys.TagTransfer), fn.toTransfer(recipient))
}

// FunctionCall is the struct containing parameters to call a function.
type FunctionCall struct {
	Name     string
	Amount   uint64
	GasLimit uint64
	Params   [][]byte
}

func (fn *FunctionCall) AddParams(params ...[]byte) {
	fn.Params = append(fn.Params, params...)
}

func (fn FunctionCall) toTransfer(recipient [32]byte) wavelet.Transfer {
	t := wavelet.Transfer{
		Recipient: recipient,
		Amount:    fn.Amount,
		GasLimit:  fn.GasLimit,
		FuncName:  []byte(fn.Name),
	}

	for _, p := range fn.Params {
		t.FuncParams = append(t.FuncParams, p...)
	}

	return t
}

func DecodeHex(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

func EncodeString(s string) []byte {
	return append([]byte(s), 0)
}

func EncodeBytes(b []byte) []byte {
	var lenbuf = make([]byte, 4)

	binary.LittleEndian.PutUint32(lenbuf, uint32(len(b)))

	return append(lenbuf, b...)
}

func EncodeByte(u byte) []byte {
	return []byte{u}
}

func EncodeUint16(u uint16) []byte {
	var buf = make([]byte, 2)

	binary.LittleEndian.PutUint16(buf, u)

	return buf
}

func EncodeUint32(u uint32) []byte {
	var buf = make([]byte, 4)

	binary.LittleEndian.PutUint32(buf, u)

	return buf
}

func EncodeUint64(u uint64) []byte {
	var buf = make([]byte, 8)

	binary.LittleEndian.PutUint64(buf, u)

	return buf
}
