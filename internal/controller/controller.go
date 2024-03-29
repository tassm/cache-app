package controller

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/TasSM/capp/internal/defs"
	"github.com/TasSM/capp/internal/svcgrpc"
)

type cacheClientController struct {
	client        defs.CacheClientService
	inputChannels map[string](*defs.TimedChannel)
	dataMutex     sync.Mutex
	svcgrpc.UnimplementedArrayBasedCacheServer
}

func NewCacheClientController(cacheClient defs.CacheClientService) svcgrpc.ArrayBasedCacheServer {
	res := &cacheClientController{
		client:        cacheClient,
		dataMutex:     sync.Mutex{},
		inputChannels: make(map[string](*defs.TimedChannel)),
	}
	go res.expirationLoop()
	return res
}

func (ctlr *cacheClientController) expirationLoop() {
	for {
		ctlr.dataMutex.Lock()
		timeStamp := time.Now().Unix()
		for k, v := range ctlr.inputChannels {
			if timeStamp >= v.Expiry {
				log.Printf("INFO - Record %v has expired", k)
				delete(ctlr.inputChannels, k)
			}
		}
		ctlr.dataMutex.Unlock()
		time.Sleep(1 * time.Second)
	}
}

func (ctlr *cacheClientController) CreateRecord(ctx context.Context, req *svcgrpc.CreateRecordRequest) (*svcgrpc.CreateRecordResponse, error) {
	ctlr.dataMutex.Lock()
	defer ctlr.dataMutex.Unlock()
	key, ttl := req.GetKey(), req.GetTtl()
	err := ctlr.client.CreateCacheArrayRecord(key, int64(ttl))
	if err != nil {
		return nil, err
	}

	expiry := time.Now().Unix() + int64(ttl)
	ctlr.inputChannels[key] = &defs.TimedChannel{DataChannel: make(chan string, 24), Expiry: expiry}
	expiryUnix := time.Now().Unix() + int64(ttl)
	go ctlr.client.Start(key, expiryUnix, ctlr.inputChannels[key].DataChannel)
	return &svcgrpc.CreateRecordResponse{Key: key, Ttl: ttl}, nil
}

func (ctlr *cacheClientController) StoreMessage(ctx context.Context, req *svcgrpc.AppendRecordRequest) (*svcgrpc.AppendRecordResponse, error) {
	key, msg := req.GetKey(), req.GetMessage()
	if ctlr.inputChannels[key] == nil {
		return nil, errors.New("Specified record has expired")
	}
	ctlr.inputChannels[key].DataChannel <- msg
	return &svcgrpc.AppendRecordResponse{Status: true}, nil
}

func (ctlr *cacheClientController) GetStatistics(ctx context.Context, req *svcgrpc.Empty) (*svcgrpc.StatisticResponse, error) {
	stats, err := ctlr.client.GetStatistics()
	if err != nil {
		log.Printf("ERROR - Retrieval of statistics from cache service failed: %v", err)
		return nil, errors.New("Failed to retrieve statistics")
	}
	return &svcgrpc.StatisticResponse{
		RecordCount:       int32(stats.RecordCount),
		ActiveConnections: int32(stats.ActiveConnections),
		LastUpdate:        stats.Timestamp,
	}, nil
}

func (ctlr *cacheClientController) GetRecord(req *svcgrpc.GetRecordRequest, stream svcgrpc.ArrayBasedCache_GetRecordServer) error {
	key := req.GetKey()
	if ctlr.inputChannels[key] == nil {
		return errors.New("Requested record has expired")
	}
	msgs, e1 := ctlr.client.ReadArrayRecord(key)
	if e1 != nil {
		panic(e1)
	}
	for i := 0; i < len(msgs); i++ {
		if e2 := stream.Send(&svcgrpc.MessageResponse{Message: msgs[i]}); e2 != nil {
			log.Printf("ERROR - Writing message %d of %d to stream failed", i+1, len(msgs))
			panic(e2)
		}
	}
	return nil
}
