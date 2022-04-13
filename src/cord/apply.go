package cord

import (
	"fmt"
	"reflect"
	"sync/atomic"

	"6.824/cord/intf"
	"6.824/proto"
	"github.com/sirupsen/logrus"
)

func (cs *CordServer) tryStartSnapshot() bool {
	if cs.maxRaftState == -1 {
		return false
	}
	cs.logger.WithFields(logrus.Fields{
		"maxstate": cs.maxRaftState * 9 / 10,
		"current":  cs.Persister.RaftStateSize(),
	}).Debug("considering snapshot")
	if cs.maxRaftState*9/10 < int64(cs.rf.GetStateSize()) {
		swapped := atomic.CompareAndSwapInt32(&cs.inSnapshot, 0, 1)
		if swapped {
			return true
		}
	}
	return false
}

func (cs *CordServer) isDuplicateReq(req *proto.RequestInfo) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	latestID, ok := cs.ack.Ack[req.ClientID]
	var ans = false
	if ok {
		ans = latestID >= req.RequestID
	}
	if !ans {
		cs.ack.Ack[req.ClientID] = req.RequestID
	}
	return ans
}

func (cs *CordServer) handleApply() {
	for msg := range cs.applyChan {
		if msg.CommandValid {
			args, ok := msg.Command.(proto.ServiceArgs)
			if !ok {
				panic("conversion error:" + reflect.TypeOf(msg.Command).String())
			}
			fmt.Println("inbound command: ", msg.CommandIndex)

			var res intf.IEvalResult
			var dump []byte
			var ss = false
			if cs.isDuplicateReq(args.Info) {
				cs.logger.WithField("Args", fmt.Sprintf("%+v", args)).Warn("duplicate request")
				res = &ProposeResult{info: args.Info}
				goto skipEval
			}

			ss = cs.tryStartSnapshot()
			res, dump = cs.kvStore.EvalCMD(&args, ss, false)
			if ss {
				cs.rf.Snapshot(msg.CommandIndex, dump)
				swapped := atomic.CompareAndSwapInt32(&cs.inSnapshot, 1, 0)
				if !swapped {
					panic("no swap")
				}
			}

		skipEval:
			cs.mu.Lock()
			ch, ok := cs.notify[int64(msg.CommandIndex)]
			if ok {
				select {
				case <-ch:
				default:
				}
			} else {
				ch = make(chan intf.IEvalResult, 1)
				cs.notify[int64(msg.CommandIndex)] = ch
			}
			cs.mu.Unlock()
			ch <- res
		} else {
			fmt.Println("Loading snapshot... size=", len(msg.Snapshot))
			cs.kvStore.LoadSnapshot(msg.Snapshot)
		}
	}
}
