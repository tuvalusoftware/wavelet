package wavelet

import (
	"bytes"
	"github.com/perlin-network/wavelet/common"
	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/blake2b"
	"math/rand"
	"testing"
	"testing/quick"
)

func TestCorrectGraphState(t *testing.T) {
	g := NewGraph()

	tx1 := randomTX(t, common.ZeroTransactionID)
	tx1.Depth = 1
	tx1.Confidence = 1

	tx2 := randomTX(t, tx1.id)
	tx2.Depth = 2
	tx2.Confidence = 2

	tx3 := randomTX(t, tx1.id, tx2.id)
	tx3.Depth = 3
	tx3.Confidence = 5

	tx4 := randomTX(t, tx3.id)
	tx4.Depth = 4
	tx4.Confidence = 6

	assert.NoError(t, g.addTransaction(tx1))
	assert.NoError(t, g.addTransaction(tx2))
	assert.NoError(t, g.addTransaction(tx3))
	assert.NoError(t, g.addTransaction(tx4))

	assert.Len(t, g.transactions, 5)
	assert.Len(t, g.children, 4)
	assert.Len(t, g.incomplete, 0)
	assert.Len(t, g.missing, 0)

	badTX1 := randomTX(t)
	badTX2 := randomTX(t)

	badTX1.ParentIDs = []common.TransactionID{tx4.id}
	badTX1.Depth = 5
	badTX1.Confidence = 7

	badTX2.ParentIDs = []common.TransactionID{badTX1.id}
	badTX2.Depth = 6
	badTX2.Confidence = 8

	// Add incomplete transaction with one missing parent.
	assert.Error(t, g.addTransaction(badTX2))
	assert.Len(t, g.transactions, 6)
	assert.Len(t, g.children, 5)
	assert.Len(t, g.incomplete, 1)
	assert.Len(t, g.missing, 1)

	// Add transaction to make last transaction inserted complete, such that there
	// is now zero missing parents.
	assert.NoError(t, g.addTransaction(badTX1))
	assert.Len(t, g.transactions, 7)
	assert.Len(t, g.children, 6)
	assert.Len(t, g.incomplete, 0)
	assert.Len(t, g.missing, 0)
}

func randomTX(t testing.TB, parents ...common.TransactionID) Transaction {
	t.Helper()

	var tx Transaction

	// Set transaction ID.
	_, err := rand.Read(tx.id[:])
	assert.NoError(t, err)

	// Set transaction sender.
	_, err = rand.Read(tx.Sender[:])
	assert.NoError(t, err)

	// Set transaction parents.
	tx.ParentIDs = parents

	// Set transaction seed.
	var buf bytes.Buffer
	_, _ = buf.Write(tx.Sender[:])
	for _, parentID := range tx.ParentIDs {
		_, _ = buf.Write(parentID[:])
	}
	seed := blake2b.Sum256(buf.Bytes())
	tx.seed = byte(prefixLen(seed[:]))

	return tx
}

func TestAddInRandomOrder(t *testing.T) {
	f := func(n int) bool {
		n = (n + 1) % 1024

		g := NewGraph()

		transactions := randomGraph(t, *g.transactions[common.ZeroTransactionID], n)
		for _, tx := range transactions {
			assert.NoError(t, g.addTransaction(tx))
		}

		if !assert.Len(t, g.transactions, len(transactions)) {
			return false
		}

		if !assert.Len(t, g.incomplete, 0) {
			return false
		}

		if !assert.Len(t, g.missing, 0) {
			return false
		}

		g = NewGraph()

		for i, j := range rand.Perm(len(transactions)) {
			transactions[i], transactions[j] = transactions[j], transactions[i]
		}

		for _, tx := range transactions {
			_ = g.addTransaction(tx)
		}

		if !assert.Len(t, g.transactions, len(transactions)) {
			return false
		}

		if !assert.Len(t, g.incomplete, 0) {
			return false
		}

		if !assert.Len(t, g.missing, 0) {
			return false
		}

		return true
	}

	assert.NoError(t, quick.Check(f, &quick.Config{MaxCount: 100}))
}

func randomGraph(t testing.TB, genesis Transaction, n int) []Transaction {
	t.Helper()

	transactions := []Transaction{genesis}

	for i := 0; i < n; i++ {
		tx := randomTX(t)

		numParents := 1 + rand.Intn(7)
		for x := 0; x < numParents; x++ {
			parent := transactions[rand.Intn(len(transactions))]

			if tx.Depth < parent.Depth {
				tx.Depth = parent.Depth
			}

			tx.Confidence += parent.Confidence + 1

			tx.ParentIDs = append(tx.ParentIDs, parent.id)
		}

		tx.Depth++

		transactions = append(transactions, tx)
	}

	return transactions
}
