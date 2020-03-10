package cross

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/simplechain-org/go-simplechain/common"
	"github.com/simplechain-org/go-simplechain/common/hexutil"
	"github.com/simplechain-org/go-simplechain/core"
	"github.com/simplechain-org/go-simplechain/core/types"
	"github.com/simplechain-org/go-simplechain/crypto"
	"github.com/simplechain-org/go-simplechain/event"
	"github.com/simplechain-org/go-simplechain/log"
	"github.com/simplechain-org/go-simplechain/p2p"
	"github.com/simplechain-org/go-simplechain/rpctx"
)

const txChanSize = 4096

type errCode int

const (
	ErrMsgTooLarge = iota
	ErrDecode
	ErrInvalidMsgCode
)

type RoleHandler int

const (
	RoleMainHandler RoleHandler = iota
	RoleSubHandler
)

func errResp(code errCode, format string, v ...interface{}) error {
	return fmt.Errorf("%v - %v", code, fmt.Sprintf(format, v...))
}

type MsgHandler struct {
	roleHandler         RoleHandler
	role                common.ChainRole
	ctxStore            CtxStore
	rtxStore            rtxStore
	blockchain          *core.BlockChain
	pm                  ProtocolManager
	crossMsgReader      <-chan interface{}
	crossMsgWriter      chan<- interface{}
	quitSync            chan struct{}
	knownRwssTx         map[common.Hash]*TranParam
	makerStartEventCh   chan core.NewCTxsEvent
	makerStartEventSub  event.Subscription
	makerSignedCh       chan core.NewCWsEvent
	makerSignedSub      event.Subscription
	takerEventCh        chan core.NewRTxsEvent
	takerEventSub       event.Subscription
	takerSignedCh       chan core.NewRWsEvent
	takerSignedSub      event.Subscription
	availableTakerCh    chan core.NewRWssEvent
	availableTakerSub   event.Subscription
	makerFinishEventCh  chan core.TransationFinishEvent
	makerFinishEventSub event.Subscription

	rtxsinLogCh         chan core.NewRTxsEvent //通过该通道删除ctx_pool中的记录，TODO 普通节点无该功能
	rtxsinLogSub        event.Subscription
	chain               simplechain
	gpo                 GasPriceOracle
	gasHelper           *GasHelper
	MainChainCtxAddress common.Address
	SubChainCtxAddress  common.Address
}

func NewMsgHandler(chain simplechain, roleHandler RoleHandler, role common.ChainRole, ctxpool CtxStore, rtxStore rtxStore,
	blockchain *core.BlockChain, crossMsgReader <-chan interface{},
	crossMsgWriter chan<- interface{}, mainAddr common.Address, subAddr common.Address) *MsgHandler {
	gasHelper := NewGasHelper(blockchain, chain)
	log.Info("NewMsgHandler", "role", role.String())
	return &MsgHandler{
		chain:               chain,
		roleHandler:         roleHandler,
		quitSync:            make(chan struct{}),
		role:                role,
		ctxStore:            ctxpool,
		rtxStore:            rtxStore,
		blockchain:          blockchain,
		crossMsgReader:      crossMsgReader,
		crossMsgWriter:      crossMsgWriter,
		gasHelper:           gasHelper,
		MainChainCtxAddress: mainAddr,
		SubChainCtxAddress:  subAddr,
		knownRwssTx:         make(map[common.Hash]*TranParam),
	}
}
func (this *MsgHandler) SetProtocolManager(pm ProtocolManager) {
	this.pm = pm
}

func (this *MsgHandler) Start() {
	this.makerStartEventCh = make(chan core.NewCTxsEvent, txChanSize)
	this.makerStartEventSub = this.blockchain.SubscribeNewCTxsEvent(this.makerStartEventCh)
	this.takerEventCh = make(chan core.NewRTxsEvent, txChanSize)
	this.takerEventSub = this.blockchain.SubscribeNewRTxsEvent(this.takerEventCh)
	this.makerSignedCh = make(chan core.NewCWsEvent, txChanSize)
	this.makerSignedSub = this.ctxStore.SubscribeCWssResultEvent(this.makerSignedCh)
	this.takerSignedCh = make(chan core.NewRWsEvent, txChanSize)
	this.takerSignedSub = this.rtxStore.SubscribeRWssResultEvent(this.takerSignedCh)
	this.availableTakerCh = make(chan core.NewRWssEvent, txChanSize)
	this.availableTakerSub = this.rtxStore.SubscribeNewRWssEvent(this.availableTakerCh)
	this.makerFinishEventCh = make(chan core.TransationFinishEvent, txChanSize)
	this.makerFinishEventSub = this.blockchain.SubscribeNewFinishsEvent(this.makerFinishEventCh)

	//单子已接
	this.rtxsinLogCh = make(chan core.NewRTxsEvent, txChanSize)
	this.rtxsinLogSub = this.blockchain.SubscribeNewRTxssEvent(this.rtxsinLogCh)

	go this.loop()

	go this.ReadCrossMessage()

}

func (this *MsgHandler) loop() {
	for {
		select {
		case ev := <-this.makerStartEventCh:
			//get cross transaction from the log
			if !this.pm.CanAcceptTxs() {
				break
			}
			if this.role.IsAnchor() {
				for _, tx := range ev.Txs {
					if err := this.ctxStore.AddLocal(tx); err != nil {
						log.Warn("Add local rtx", "err", err)
					}
				}
				this.pm.BroadcastCtx(ev.Txs)
			}
		case <-this.makerStartEventSub.Err():
			return
		case ev := <-this.makerSignedCh:
			this.pm.BroadcastInternalCrossTransactionWithSignature([]*types.CrossTransactionWithSignatures{ev.Txs}) //主网广播
			if this.role.IsAnchor() {
				this.WriteCrossMessage(ev.Txs) //发送到子网
			}
		case <-this.makerSignedSub.Err():
			return

		case ev := <-this.availableTakerCh:
			if this.role.IsAnchor() {
				if len(ev.Tws) == 0 {
					for k := range this.knownRwssTx { //清理缓存
						delete(this.knownRwssTx, k)
					}
				}
				key, err := rpctx.StringToPrivateKey(rpctx.PrivateKey)
				if err != nil {
					log.Error("GetTxForLockOut", "err", err)
					break
				}
				address := crypto.PubkeyToAddress(key.PublicKey)
				if pending, err := this.pm.GetAnchorTxs(address); err == nil && len(pending) < 10 {
					gasUsed, _ := new(big.Int).SetString("80000000000000", 10) //todo gasUsed
					txs, err := this.GetTxForLockOut(ev.Tws, gasUsed)
					if err != nil {
						log.Info("availableTakerCh", "err", err)
						//记录延迟，不能删
						// this.rtxStore.RemoveLocals([]*types.ReceptTransactionWithSignatures{rws})
					}
					this.pm.AddRemotes(txs)
				}
			}
		case <-this.availableTakerSub.Err():
			return
		case ev := <-this.takerEventCh:
			if !this.pm.CanAcceptTxs() {
				break
			}
			if this.role.IsAnchor() {
				for _, tx := range ev.Txs {
					if err := this.rtxStore.AddLocal(tx); err != nil {
						log.Warn("Add local rtx", "err", err)
					}
				}
				this.pm.BroadcastRtx(ev.Txs)
			}
		case <-this.takerEventSub.Err():
			return
		case ev := <-this.takerSignedCh:
			if this.role.IsAnchor() {
				this.WriteCrossMessage(ev.Tws)
			}
		case <-this.takerSignedSub.Err():
			return
		case ev := <-this.rtxsinLogCh:
			this.ctxStore.RemoveRemotes(ev.Txs) //删除本地待接单
		case <-this.rtxsinLogSub.Err():
			return
		case ev := <-this.makerFinishEventCh:
			if err := this.clearStore(ev.Finish); err != nil {
				log.Info("clearStore", "err", err)
			}
		case <-this.makerFinishEventSub.Err():
			return

		}
	}
}

func (this *MsgHandler) Stop() {
	log.Info("Stopping Simplechain MsgHandler")
	this.makerStartEventSub.Unsubscribe()
	this.makerSignedSub.Unsubscribe()
	this.takerEventSub.Unsubscribe()
	this.takerSignedSub.Unsubscribe()
	this.rtxsinLogSub.Unsubscribe()
	this.availableTakerSub.Unsubscribe()
	close(this.quitSync)
	log.Info("Simplechain MsgHandler stopped")
}

func (this *MsgHandler) HandleMsg(msg p2p.Msg, p Peer) error {
	switch {
	case msg.Code == CtxSignMsg:
		if !this.pm.CanAcceptTxs() {
			break
		}
		var ctx *types.CrossTransaction
		if err := msg.Decode(&ctx); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		if err := this.ctxStore.ValidateCtx(ctx); err == nil {
			p.MarkReceptTransaction(ctx.SignHash())
			//todo
			this.pm.BroadcastCtx([]*types.CrossTransaction{ctx})
			if err := this.ctxStore.AddRemote(ctx); err != nil {
				log.Debug("Add remote ctx", "err", err)
			}
		}
	case msg.Code == CtxSignsMsg:
		if !this.pm.CanAcceptTxs() {
			break
		}
		var cwss []*types.CrossTransactionWithSignatures
		if err := msg.Decode(&cwss); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}

		this.ctxStore.AddCWss(cwss)
		this.pm.BroadcastCWss(cwss)
		for _, cws := range cwss {
			p.MarkCrossTransactionWithSignatures(cws.ID())
		}

	case msg.Code == RtxSignMsg:
		if !this.pm.CanAcceptTxs() {
			break
		}
		var rtx *types.ReceptTransaction
		if err := msg.Decode(&rtx); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		if err := this.rtxStore.ValidateRtx(rtx); err == nil {
			p.MarkReceptTransaction(rtx.SignHash())
			this.pm.BroadcastRtx([]*types.ReceptTransaction{rtx})
			if err := this.rtxStore.AddRemote(rtx); err != nil {
				//log.Warn("Add remote rtx", "err", err)
			}
		} else {
			//log.Warn("Add remote rtx", "err", err)
			break
		}

	case msg.Code == CtxSignsInternalMsg:
		//if !this.pm.CanAcceptTxs() {
		//	break
		//}
		var cwss []*types.CrossTransactionWithSignatures
		if err := msg.Decode(&cwss); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		//Receive and broadcast
		this.ctxStore.AddCWss(cwss)
		this.pm.BroadcastInternalCrossTransactionWithSignature(cwss)
		for _, cws := range cwss {
			p.MarkInternalCrossTransactionWithSignatures(cws.ID())
		}
	//case msg.Code == GetCtxSignsMsg:
	//	var Query GetCtxSignsData
	//
	//	if err := msg.Decode(&Query); err != nil {
	//		return errResp(ErrDecode, "%v: %v", msg, err)
	//	}
	//	cwss := this.ctxStore.List(Query.Amount, Query.GetAll)
	//	p.SendCrossTransactionWithSignatures(cwss)

	default:
		return errResp(ErrInvalidMsgCode, "%v", msg.Code)
	}
	return nil
}

func (this *MsgHandler) ReadCrossMessage() {
	for {
		select {
		case v := <-this.crossMsgReader:
			//log.Info("ReadCrossMessage")
			cws, ok := v.(*types.CrossTransactionWithSignatures)
			//if ok {
			//	log.Info("ReadCrossMessage", "networkID", this.pm.NetworkId(), "destId", cws.Data.DestinationId.Uint64())
			//}
			if ok && cws.Data.DestinationId.Uint64() == this.pm.NetworkId() {
				this.ctxStore.AddCWss([]*types.CrossTransactionWithSignatures{cws})
				this.pm.BroadcastCWss([]*types.CrossTransactionWithSignatures{cws})
				break
			}
			rws, ok := v.(*types.ReceptTransactionWithSignatures)
			//if ok {
			//	log.Info("ReadCrossMessage", "networkID", this.pm.NetworkId(), "destId", rws.Data.DestinationId.Uint64())
			//}
			if ok && rws.Data.DestinationId.Uint64() == this.pm.NetworkId() {
				if v := this.rtxStore.ReadFromLocals(rws.Data.CTxId); v == nil {
					errs := this.rtxStore.AddLocals(rws)
					for _, err := range errs {
						if err != nil {
							log.Error("MsgHandler signed rtx save error")
							break
						}
					}
				}
				//log.Info("send tx for rtx")
				//gasUsed, _ := new(big.Int).SetString("300000000000000", 10) //todo gasUsed
				//tx, err := this.GetTxForLockOut(rws, gasUsed, this.pm.NetworkId())
				//if err != nil {
				//	log.Info("ReadCrossMessage", "err", err)
				//	break
				//}
				//
				////锚定节点本地存储
				//this.pm.AddRemotes([]*types.Transaction{tx})
				break
			}
		case <-this.quitSync:
			return
		}
	}
}

func (this *MsgHandler) GetTxForLockOut(rwss []*types.ReceptTransactionWithSignatures, gasUsed *big.Int) ([]*types.Transaction, error) {

	// nonce
	key, err := rpctx.StringToPrivateKey(rpctx.PrivateKey)
	if err != nil {
		log.Error("GetTxForLockOut", "err", err)
		return nil, err
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	//TODO stateDB一致性
	nonce := this.pm.GetNonce(address)

	var txs []*types.Transaction
	var errorRws []*types.ReceptTransactionWithSignatures
	var count, send, exec, errTx1, errTx2 uint64
	tokenAddress := this.GetContractAddress()

	for _, rws := range rwss {
		//TODO EstimateGas不仅测试GasLimit，同时能判断该交易是否执行成功
		var tx *types.Transaction
		var param *TranParam
		if _, ok := this.knownRwssTx[rws.ID()]; !ok {
			param, err = this.CreateTransaction(key, address, rws, gasUsed)
			if err != nil {
				//log.Error("CreateTransaction1", "err", err)
				errorRws = append(errorRws, rws)
				errTx1++
				continue
			}
			this.knownRwssTx[rws.ID()] = param
		} else { //TODO delete
			param = this.knownRwssTx[rws.ID()]
			if ok, _ := this.CheckTransaction(key, address, tokenAddress, rws, gasUsed, nonce+count, param.gasLimit, param.gasPrice, param.data); !ok {
				errorRws = append(errorRws, rws)
				exec++
				//log.Info("Check","err",err,"ok",ok,"ctxID",rws.ID().String())
				continue
			} else {
				send++
			}
		}

		tx, err = NewSignedTransaction(nonce+count, tokenAddress, param.gasLimit, param.gasPrice, param.data, this.pm.NetworkId(), key)
		if err != nil {
			return nil, err
		}

		txs = append(txs, tx)
		count++
		if len(txs) >= 200 {
			break
		}
		if count >= 1024 { //TODO
			break
		}
	}
	log.Info("GetTxForLockOut", "errorRws", len(errorRws), "exec", exec, "errtx1", errTx1, "errtx2", errTx2, "tx", len(txs), "sent", send, "role", this.role.String())
	return txs, nil
}

func (this *MsgHandler) WriteCrossMessage(v interface{}) {
	select {
	case this.crossMsgWriter <- v:
	case <-this.quitSync:
		return
	}
}

func (this *MsgHandler) clearStore(finishes []*types.FinishInfo) error {
	if err := this.ctxStore.RemoveLocals(finishes); err != nil {
		return errors.New("rm ctx error")
	}
	if err := this.rtxStore.RemoveLocals(finishes); err != nil {
		return errors.New("rm rtx error")
	}
	return nil
}

func (this *MsgHandler) GetContractAddress() common.Address {
	var tokenAddress common.Address
	switch this.roleHandler {
	case RoleMainHandler:
		tokenAddress = this.MainChainCtxAddress
	case RoleSubHandler:
		tokenAddress = this.SubChainCtxAddress
	}
	return tokenAddress
}
func (this *MsgHandler) SetGasPriceOracle(gpo GasPriceOracle) {
	this.gpo = gpo
}
func (this *MsgHandler) CreateTransaction(key *ecdsa.PrivateKey, address common.Address, rws *types.ReceptTransactionWithSignatures, gasUsed *big.Int) (*TranParam, error) {
	tokenAddress := this.GetContractAddress()
	gasPrice, err := this.gpo.SuggestPrice(context.Background())

	if err != nil {
		return nil, err
	}
	data, err := rws.ConstructData(gasUsed)
	if err != nil {
		log.Info("ConstructData", "err", err)
		return nil, err
	}

	//log.Info("data","data",data,"rws",rws,"chainid",rws.ChainId().String())
	callArgs := CallArgs{
		From:     address,
		To:       &tokenAddress,
		Data:     data,
		GasPrice: hexutil.Big(*gasPrice),
	}

	gasLimit, err := this.gasHelper.EstimateGas(context.Background(), callArgs)
	if err != nil {
		return nil, err
	}

	//gasPrice.Add(gasPrice,big.NewInt(1000000000))

	return &TranParam{gasLimit: gasLimit, gasPrice: gasPrice, data: data}, nil
}

func (this *MsgHandler) CheckTransaction(key *ecdsa.PrivateKey, address, tokenAddress common.Address, rws *types.ReceptTransactionWithSignatures, gasUsed *big.Int, nonce, gasLimit uint64, gasPrice *big.Int, data []byte) (bool, error) {
	callArgs := CallArgs{
		From:     address,
		To:       &tokenAddress,
		Data:     data,
		GasPrice: hexutil.Big(*gasPrice),
		Gas:      hexutil.Uint64(gasLimit),
	}
	return this.gasHelper.CheckExec(context.Background(), callArgs)
}

func (this *MsgHandler) GetCtxstore() CtxStore {
	return this.ctxStore
}

type TranParam struct {
	gasLimit uint64
	gasPrice *big.Int
	data     []byte
}

type GetCtxSignsData struct {
	Amount int  // Maximum number of headers to retrieve
	GetAll bool // Query all
}
