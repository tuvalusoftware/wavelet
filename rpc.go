package wavelet

import (
	"encoding/hex"
	"github.com/perlin-network/graph/database"
	"github.com/perlin-network/graph/system"
	"github.com/perlin-network/graph/wire"
	"github.com/perlin-network/wavelet/security"
	"github.com/phf/go-queue/queue"
	"github.com/pkg/errors"
)

type rpc struct {
	*Ledger
}

// RespondToQuery provides a response should we be selected as one of the K peers
// that votes whether or not we personally strongly prefer a transaction which we
// receive over the wire.
//
// Our response is `true` should we strongly prefer a transaction, or `false` otherwise.
func (r *rpc) RespondToQuery(wired *wire.Transaction) (string, bool, error) {
	if validated, err := security.ValidateWiredTransaction(wired); err != nil || !validated {
		return "", false, errors.Wrap(err, "failed to validate incoming tx")
	}

	senderID, err := hex.DecodeString(wired.Sender)
	if err != nil {
		return "", false, errors.Wrap(err, "failed to decode sender id")
	}

	account, err := r.LoadAccount(senderID)
	if err != nil {
		if wired.Nonce != 0 {
			return "", false, errors.Wrap(err, "tx sender account not found")
		}
	}

	// If the nonce of the transaction is less than the currently accepted accounts nonce, reject it. Prevents most double spending
	// cases from even reaching a conflict set.
	if account != nil && wired.Nonce < account.Nonce {
		return "", false, errors.Wrap(err, "tx nonce is outdated in comparison to the actual accounts nonce")
	}

	id, err := r.Receive(wired)

	if err != nil {
		return "", false, errors.Wrap(err, "failed to add incoming tx to graph")
	}

	return id, r.IsStronglyPreferred(id), nil
}

// HandleSuccessfulQuery updates the conflict sets and acceptance of all transactions
// preceding a successfully queried transactions.
func (r *rpc) HandleSuccessfulQuery(tx *database.Transaction) error {
	visited := make(map[string]struct{})

	queue := queue.New()
	queue.PushBack(tx.Id)

	for queue.Len() > 0 {
		popped := queue.PopFront().(string)

		// This line cuts down consensus time from 0.03 seconds to 0.01 seconds.
		// Whether or not it's correct requires an analysis of its own.
		if r.WasAccepted(popped) {
			continue
		}

		tx, err := r.GetBySymbol(popped)
		if err != nil {
			continue
		}

		set, err := r.GetConflictSet(tx.Sender, tx.Nonce)
		if err != nil {
			continue
		}

		score, preferredScore := r.CountAscendants(popped, system.Beta2), r.CountAscendants(set.Preferred, system.Beta2)

		if score > preferredScore {
			set.Preferred = popped
		}

		if popped != set.Last {
			set.Last = popped
			set.Count = 0
		} else {
			set.Count++
		}

		err = r.SaveConflictSet(tx.Sender, tx.Nonce, set)
		if err != nil {
			continue
		}

		for _, parent := range tx.Parents {
			if _, seen := visited[parent]; !seen {
				visited[parent] = struct{}{}

				queue.PushBack(parent)
			}
		}
	}

	return nil
}