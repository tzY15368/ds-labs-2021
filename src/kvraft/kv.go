package kvraft

import (
	"sync"

	"github.com/sirupsen/logrus"
)

// put 和 append的int参数用于指定期待的index，该index来自kv.rf.start
type KVInterface interface {
	// EvalOp return value for GET, and "" for PUT/APPEND
	EvalOp(Op) OPResult
}

type SimpleKVStore struct {
	data   map[string]string
	mu     sync.Mutex
	logger *logrus.Entry
	ack    map[int64]int64 // client's latest request id (for deduplication)
}

func NewKVStore(logger *logrus.Entry) KVInterface {
	sks := &SimpleKVStore{
		data:   make(map[string]string),
		logger: logger,
		ack:    make(map[int64]int64),
	}
	logger.Debug("kvstore: started kvstore")
	return sks
}

func (sk *SimpleKVStore) isDuplicate(request RequestInfo) bool {
	latestRequestId, ok := sk.ack[request.ClientID]
	if ok {
		return latestRequestId >= request.RequestID
	}
	return false
}

func (sk *SimpleKVStore) EvalOp(op Op) OPResult {
	sk.mu.Lock()
	defer sk.mu.Unlock()
	opRes := OPResult{
		err:         nil,
		requestInfo: op.RequestInfo,
	}
	if sk.isDuplicate(op.RequestInfo) {
		if op.OpType == OP_GET {
			opRes.data = sk.data[op.OpKey]
		}
		return opRes
	}

	// register in ack
	sk.ack[op.RequestInfo.ClientID] = op.RequestInfo.RequestID

	switch op.OpType {
	case OP_GET:
		data, ok := sk.data[op.OpKey]
		if ok {
			opRes.data = data
			return opRes
		} else {
			opRes.err = ErrKeyNotFound
			return opRes
		}
	case OP_PUT:
		sk.data[op.OpKey] = op.OPValue
	case OP_APPEND:
		sk.data[op.OpKey] += op.OPValue
	}
	return opRes
}
