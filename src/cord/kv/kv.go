package kv

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"6.824/cord/cdc"
	"6.824/logging"
	"6.824/proto"
	"github.com/sirupsen/logrus"
)

// 需要一个FIFO的RWLOCK
type TempKVStore struct {
	mu                 sync.RWMutex
	dataStore          proto.TempKVStore
	ack                proto.AckMap
	dataChangeHandlers DataChangeHandler
	logger             *logrus.Logger
}

type DataChangeHandler interface {
	CaptureDataChange(string, string)
	Watch(string) (*cdc.WatchResult, error)
}

type EvalResult struct {
	Err     error
	Data    map[string]string
	Info    *proto.RequestInfo
	Watches []*cdc.WatchResult
}

var ErrGetOnly = errors.New("err get only in unserializable reads")
var ErrNoWatch = errors.New("err watches not enabled")
var ErrNotImpl = errors.New("err not impl")

func NewTempKVStore() *TempKVStore {
	tks := &TempKVStore{
		dataStore: proto.TempKVStore{Data: make(map[string]*proto.KVEntry)},
		ack:       proto.AckMap{Ack: make(map[int64]int64)},
		logger:    logging.GetLogger("kvs", logrus.InfoLevel),
	}
	return tks
}

func (kvs *TempKVStore) SetCDC(dch DataChangeHandler) {
	kvs.dataChangeHandlers = dch
}

func (kvs *TempKVStore) EvalCMDUnlinearizable(args *proto.ServiceArgs) *EvalResult {
	reply := &EvalResult{
		Data: make(map[string]string),
	}
	kvs.mu.RLock()
	for _, cmd := range args.Cmds {
		if cmd.OpType != proto.CmdArgs_GET {
			reply.Err = ErrGetOnly
			break
		}
		reply.Data[cmd.OpKey] = kvs.dataStore.Data[cmd.OpKey].Data
	}
	defer kvs.mu.RUnlock()
	return reply
}

func (kvs *TempKVStore) isDuplicate(req *proto.RequestInfo) bool {
	kvs.mu.Lock()
	defer kvs.mu.Unlock()
	latestID, ok := kvs.ack.Ack[req.ClientID]
	var ans = false
	if ok {
		ans = latestID >= req.RequestID
	}
	if !ans {
		kvs.ack.Ack[req.ClientID] = req.RequestID
	}
	return ans
}

func dataExpired(ttl int64) bool {
	if ttl == 0 {
		return false
	}
	now := time.Now().UnixNano() / 1e6
	return ttl < now
}

func (kvs *TempKVStore) EvalCMD(args *proto.ServiceArgs, shouldSnapshot bool) (reply *EvalResult, dump []byte) {
	reply = &EvalResult{
		Data:    make(map[string]string),
		Info:    args.Info,
		Watches: make([]*cdc.WatchResult, 0),
	}
	if kvs.isDuplicate(args.Info) {
		kvs.logger.WithField("Args", fmt.Sprintf("%+v", args)).Warn("duplicate request")
		return
	}
	var lockWrite = shouldSnapshot
	if !shouldSnapshot {
		for _, cmd := range args.Cmds {
			if cmd.OpType != proto.CmdArgs_GET && cmd.OpType != proto.CmdArgs_WATCH {
				lockWrite = true
				break
			}
		}
	}
	if lockWrite {
		kvs.mu.Lock()
		defer kvs.mu.Unlock()
	} else {
		kvs.mu.RLock()
		defer kvs.mu.RUnlock()
	}
	for _, cmd := range args.Cmds {
		switch cmd.OpType {
		case proto.CmdArgs_GET:
			if len(cmd.OpKey) > 1 && cmd.OpKey[len(cmd.OpKey)-1] == '*' {
				for key, entry := range kvs.dataStore.Data {
					if len(key) >= len(cmd.OpKey)-1 && key[:len(cmd.OpKey)-1] == cmd.OpKey[:len(cmd.OpKey)-1] {
						if dataExpired(entry.Ttl) {
							fmt.Println("ttl reached")
							delete(kvs.dataStore.Data, key)
						} else {
							reply.Data[cmd.OpKey] = entry.Data

						}
					}
				}
				return
			}
			entry := kvs.dataStore.Data[cmd.OpKey]
			reply.Data[cmd.OpKey] = ""
			if entry != nil {
				if dataExpired(entry.Ttl) {
					fmt.Println("ttl reached")
					delete(kvs.dataStore.Data, cmd.OpKey)
				} else {
					reply.Data[cmd.OpKey] = entry.Data
				}
			}
		case proto.CmdArgs_APPEND:
			old := kvs.dataStore.Data[cmd.OpKey]
			didChange := cmd.OpVal != ""
			if old != nil {
				kvs.dataStore.Data[cmd.OpKey].Data += cmd.OpVal
				kvs.dataStore.Data[cmd.OpKey].Ttl = cmd.Ttl
			} else {
				kvs.dataStore.Data[cmd.OpKey] = &proto.KVEntry{Data: cmd.OpVal, Ttl: cmd.Ttl}
			}
			if kvs.dataChangeHandlers != nil && didChange {
				kvs.dataChangeHandlers.CaptureDataChange(cmd.OpKey, kvs.dataStore.Data[cmd.OpKey].Data)
			}
		case proto.CmdArgs_PUT:
			didChange := false
			entry := kvs.dataStore.Data[cmd.OpKey]
			if entry != nil && entry.Data != cmd.OpVal {
				didChange = true
			}
			kvs.dataStore.Data[cmd.OpKey] = &proto.KVEntry{Data: cmd.OpVal, Ttl: cmd.Ttl}
			if kvs.dataChangeHandlers != nil && didChange {
				kvs.dataChangeHandlers.CaptureDataChange(cmd.OpKey, cmd.OpVal)
			}
		case proto.CmdArgs_WATCH:
			if kvs.dataChangeHandlers == nil {
				reply.Err = ErrNoWatch
				return
			}
			watchResult, err := kvs.dataChangeHandlers.Watch(cmd.OpKey)
			if err != nil {
				reply.Err = err
			} else {
				reply.Watches = append(reply.Watches, watchResult)
			}
		case proto.CmdArgs_DELETE:
			// 删除不存在的key不应该唤醒watch
			entry := kvs.dataStore.Data[cmd.OpKey]
			if entry != nil && kvs.dataChangeHandlers != nil {
				kvs.dataChangeHandlers.CaptureDataChange(cmd.OpKey, "")
			}

		default:
			reply.Err = ErrNotImpl
			return
		}
	}
	if shouldSnapshot {
		dump, reply.Err = kvs.dataStore.Marshal()
		fmt.Println("snapshot: got dump bytes", len(dump))
	}
	return
}

func (kvs *TempKVStore) LoadSnapshot(data []byte) {
	err := kvs.dataStore.Unmarshal(data)
	if err != nil {
		panic(err)
	}
}
