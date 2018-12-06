package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/pingcap/kvproto/pkg/metapb"
	"github.com/pingcap/pd/server/core"
)

var event chan Event
var m map[uint64]int
var mSize map[uint64]int64
var mutex = &sync.Mutex{}
var mutexSize = &sync.Mutex{}
var cfg *Config

type PeerCreateEvent struct {
	TiKVId   uint64 `json:"tikv_id"`
	RegionId uint64 `json:"region_id"`
	PeerId   uint64 `json:"peer_id"`
	StartKey string `json:"region_start_key"`
	EndKey   string `json:"region_end_key"`
}

type PeerDestroyEvent struct {
	TiKVId   uint64 `json:"tikv_id"`
	RegionId uint64 `json:"region_id"`
	PeerId   uint64 `json:"peer_id"`
}

type RegionSizeChangeEvent struct {
	TiKVId   uint64 `json:"tikv_id"`
	RegionId uint64 `json:"region_id"`
	PeerId   uint64 `json:"peer_id"`
	NewSize  int64  `json:"new_size"`
}

type RegionSplitEvent struct {
	TiKVId        uint64 `json:"tikv_id"`
	LeftRegionId  uint64 `json:"left_region_id"`
	RightRegionId uint64 `json:"right_region_id"`
	StartKey      string `json:"region_start_key"`
	EndKey        string `json:"region_end_key"`
	SplitKey      string `json:"region_split_key"`
}

type Event struct {
	TS        int64       `json:"timestamp"`
	EvId      int         `json:"eid"`
	EventName string      `json:"event_name""`
	Payload   interface{} `json:"payload""`
}

func (e Event) toString() string {
	bytes, err := json.Marshal(e)
	if err == nil {
		return string(bytes)
	} else {
		panic(err)
	}
}

func pushEvent(e Event) {
	if cfg == nil || len(cfg.LabAddress) == 0 {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	resp, err := http.Post(fmt.Sprintf("http://%s/event", cfg.LabAddress), "application/json", bytes.NewBuffer(data))
	defer resp.Body.Close()
	if err != nil {
		fmt.Println(err)
	}
}

func AddPeerDestroyEvent(tikvId uint64, regionId uint64, peerId uint64) {
	ts := time.Now().UnixNano() / 1000000
	event <- Event{
		TS:        ts,
		EventName: "TiKVPeerDestroy",
		Payload: &PeerDestroyEvent{
			TiKVId:   tikvId,
			RegionId: regionId,
			PeerId:   peerId,
		},
	}
}

func AddRegionSizeChange(tikvId uint64, peerId uint64, regionId uint64, newSize int64) {
	ts := time.Now().UnixNano() / 1000000
	event <- Event{
		TS:        ts,
		EventName: "TiKVRegionSizeChange",
		Payload: &RegionSizeChangeEvent{
			TiKVId:   tikvId,
			RegionId: regionId,
			NewSize:  newSize,
		},
	}
}

func AddRegionSplit(left *metapb.Region, right *metapb.Region) {
	AddPeerDirectProto(left)
	AddPeerDirectProto(right)
	for _, peer := range left.Peers {
		addRegionSplit(peer.StoreId, left.Id, right.Id, left.StartKey, right.EndKey, right.StartKey)
	}
}

func addRegionSplit(storeId uint64, leftRegionId uint64, rightRegionId uint64, startKey []byte, endKey []byte, splitKey []byte) {
	ts := time.Now().UnixNano() / 1000000
	event <- Event{
		TS:        ts,
		EventName: "TiKVRegionSplit",
		Payload: &RegionSplitEvent{
			TiKVId:        storeId,
			LeftRegionId:  leftRegionId,
			RightRegionId: rightRegionId,
			StartKey:      hex.EncodeToString(startKey),
			EndKey:        hex.EncodeToString(endKey),
			SplitKey:      hex.EncodeToString(splitKey),
		},
	}
}

func AddPeerCreateEvent(origin *core.RegionInfo, other *core.RegionInfo) {
	for _, a := range origin.GetPeers() {
		both := false
		for _, b := range other.GetPeers() {
			if reflect.DeepEqual(a, b) {
				both = true
				break
			}
		}
		if !both {
			if !a.IsLearner {
				AddPeerDestroyEvent(a.StoreId, origin.GetID(), a.Id)
			}
		}
	}
	for _, b := range other.GetPeers() {
		both := false
		for _, a := range origin.GetPeers() {
			if reflect.DeepEqual(a, b) {
				both = true
				break
			}
		}
		if !both {
			if !b.IsLearner {
				addPeerCreateEvent(b.StoreId, origin.GetID(), b.Id, origin.GetStartKey(), origin.GetEndKey())
			}
		}
	}
}

func AddPeerDirect(r *core.RegionInfo) {
	for _, p := range r.GetPeers() {
		addPeerCreateEvent(p.StoreId, r.GetID(), p.Id, r.GetStartKey(), r.GetEndKey())
	}
}

func AddPeerDirectProto(r *metapb.Region) {
	for _, p := range r.Peers {
		addPeerCreateEvent(p.StoreId, r.Id, p.Id, r.StartKey, r.EndKey)
	}
}

func addPeerCreateEvent(tikvId uint64, regionId uint64, peerId uint64, startKey []byte, endKey []byte) {
	ts := time.Now().UnixNano() / 1000000
	mutex.Lock()
	defer mutex.Unlock()
	_, ok := m[peerId]
	if ok {
		return
	}
	m[peerId] = 0
	event <- Event{
		TS:        ts,
		EventName: "TiKVPeerCreate",
		Payload: &PeerCreateEvent{
			TiKVId:   tikvId,
			RegionId: regionId,
			PeerId:   peerId,
			StartKey: hex.EncodeToString(startKey),
			EndKey:   hex.EncodeToString(endKey)},
	}
}

func RefreshSize(origin *core.RegionInfo, size int64) {
	mutexSize.Lock()
	defer mutexSize.Unlock()
	oldSize, ok := mSize[origin.GetID()]
	if ok {
		if oldSize != size {
			for _, p := range origin.GetPeers() {
				AddRegionSizeChange(p.StoreId, p.Id, origin.GetID(), size)
			}
		}
	}
	mSize[origin.GetID()] = size
}

type Peer struct {
	PeerId  uint64
	StoreId uint64
}

type PeerDiffResult struct {
	Added   []*Peer
	Removed []*Peer
}

func runLabEventPusher() {
	for {
		var e Event
		e = <-event
		pushEvent(e)
	}
}

func InitLab(cfg *Config) {
	event = make(chan Event)
	m = make(map[uint64]int)
	mSize = make(map[uint64]int64)
	go runLabEventPusher()
}
