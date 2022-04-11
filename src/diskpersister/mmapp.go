package diskpersister

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"

	"github.com/edsrzf/mmap-go"
)

type MMapPersister struct {
	raftFile     *os.File
	snapshotFile *os.File
	raftMem      mmap.MMap
	snapshotMem  mmap.MMap
	rwmu         sync.RWMutex
	// maxsize(bytes)
	maxSize          int64
	currentLogOffset int64
}

func (mp *MMapPersister) PersistInt64(data int64, offset int64) {
	if offset < 0 || offset > 3 {
		panic("wrong offset")
	}
	casted := make([]byte, 8)
	binary.LittleEndian.PutUint64(casted, uint64(data))
	n := copy(mp.raftMem[8*offset:8*(offset+1)], casted)
	if n != 8 {
		panic("bad persist")
	}
}

// func (mp *MMapPersister) AppendToRaftLog(data []byte, lastIncludedOffset)

// maxsize in bytes
func NewMMapPersister(raftFname string, snapshotFname string, maxSize int64) *MMapPersister {
	filerf, err := os.OpenFile(raftFname, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		panic(err)
	}
	stat, err := filerf.Stat()
	if err != nil {
		panic(err)
	}
	if stat.Size() >= maxSize {
		fmt.Println("warning: size > maxsize, will NOT truncate")
	} else {
		err = filerf.Truncate(int64(maxSize))
		if err != nil {
			panic(err)
		}
	}
	memrf, err := mmap.Map(filerf, mmap.RDWR, 0)
	if err != nil {
		panic(err)
	}

	// snapshot...
	const DEFAULT_SNAPSHOT_SIZE int64 = 1e6
	filess, err := os.OpenFile(snapshotFname, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		panic(err)
	}
	stat, err = filess.Stat()
	if err != nil {
		panic(err)
	}
	if stat.Size() >= DEFAULT_SNAPSHOT_SIZE {
		fmt.Println("warning: size > maxsize, will NOT truncate")
	} else {
		err = filess.Truncate(DEFAULT_SNAPSHOT_SIZE)
		if err != nil {
			panic(err)
		}
	}

	memss, err := mmap.Map(filess, mmap.RDWR, 0)
	if err != nil {
		panic(err)
	}
	return &MMapPersister{
		raftFile:         filerf,
		raftMem:          memrf,
		snapshotFile:     filess,
		snapshotMem:      memss,
		maxSize:          maxSize,
		currentLogOffset: 0,
	}
}

func (mp *MMapPersister) Close() {
	mp.rwmu.Lock()
	defer mp.rwmu.Unlock()
	mp.raftMem.Unmap()
	mp.raftFile.Close()
}

func (mp *MMapPersister) SaveStateAndSnapshot(state []byte, snapshot []byte) {
	mp.rwmu.Lock()
	defer mp.rwmu.Unlock()
	var err error
	err = Write(mp.raftFile, &mp.raftMem, &state, false)
	if err != nil {
		panic(err)
	}

	err = Write(mp.snapshotFile, &mp.snapshotMem, &snapshot, true)
	if err != nil {
		panic(err)
	}
}

// primitive write, not thread safe
// 超过一半时候扩容*2， 小于1/4则缩容/2
// 假设；snapshot每次变更幅度不大
func Write(fd *os.File, mem **mmap.MMap, data *[]byte, allowResize bool) error {
	actualLen := len(*data) + 8
	if !allowResize && actualLen > len(*mem) {
		panic("too long, cant write")
	}
	if len(*mem)/2 < actualLen {
		// enlarge
		// 老数据找不到了不要紧，write会重写新的长度在最后
		err := fd.Truncate(int64(2 * len(*data)))
		if err != nil {
			return err
		}
	} else if len(*mem) > actualLen*4 {
		// shrink
		err := fd.Truncate(int64(actualLen) * 2)
		if err != nil {
			return err
		}

	}
	n := copy(*mem, *data)
	if n != len(*data) {
		panic("wrong len")
	}

	casted := make([]byte, 8)
	binary.LittleEndian.PutUint64(casted, uint64(len(*data)))

	n = copy((*mem)[len(*mem)-8:], casted)
	if n != 8 {
		panic("wrong len 8")
	}
	return nil
}

// primitive read, not thread safe
func Read(mem *mmap.MMap) (*[]byte, error) {
	lenOfData := binary.LittleEndian.Uint64((*mem)[len(*mem)-8:])
	result := make([]byte, lenOfData)
	n := copy(result, *mem)
	if uint64(n) != lenOfData {
		panic("bad read")
	}
	return &result, nil
}

func Size(mem *mmap.MMap) int {
	return int(binary.LittleEndian.Uint64((*mem)[len(*mem)-8:]))
}

func (mp *MMapPersister) ReadRaftState() []byte {
	mp.rwmu.RLock()
	defer mp.rwmu.RUnlock()
	data, err := Read(&mp.raftMem)
	if err != nil {
		panic(err)
	}
	return *data
}

func (mp *MMapPersister) ReadSnapshot() []byte {
	mp.rwmu.RLock()
	defer mp.rwmu.RUnlock()
	data, err := Read(&mp.snapshotMem)
	if err != nil {
		panic(err)
	}
	return *data
}

func (mp *MMapPersister) RaftStateSize() int {
	mp.rwmu.RLock()
	defer mp.rwmu.RUnlock()
	return Size(&mp.raftMem)
}

func (mp *MMapPersister) SnapshotSize() int {
	mp.rwmu.RLock()
	defer mp.rwmu.RUnlock()
	return Size(&mp.snapshotMem)
}