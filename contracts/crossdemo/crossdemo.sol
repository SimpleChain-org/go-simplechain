pragma solidity ^0.6.0;
pragma experimental ABIEncoderV2;
contract crossDemo{
    //合约管理员
    address public owner;

    //其他链的信息
    mapping (uint => Chain) public crossChains;

    //仅做信息登记，关联chainId
    struct Chain{
        uint remoteChainId;
        uint8 signConfirmCount;//最少签名数量
        uint64 anchorsPositionBit;// 锚定节点 二进制表示 例如 1101001010, 最多62个锚定节点，空余位置0由外部计算
        address[] anchorAddress;
        mapping(address=>Anchor) anchors;   //锚定矿工列表 address => miners
        mapping(bytes32=>uint256) makerTxs; //挂单 交易完成后删除交易，通过发送日志方式来呈现交易历史。
        mapping(bytes32=>uint256) takerTxs; //跨链交易列表 吃单 hash => Transaction[]
    }

    struct Anchor {
        uint remoteChainId;
        uint8 position; // anchorsPositionBit
    }

    //创建交易 maker
    event MakerTx(bytes32 indexed txId, address indexed from, uint remoteChainId, uint value, uint destValue,bytes data);

    event MakerFinish(bytes32 indexed txId, address indexed to);
    //达成交易 taker
    event TakerTx(bytes32 indexed txId, address indexed to, uint remoteChainId, address from,uint value, uint destValue,bytes input);

    event AddAnchors(uint remoteChainId);

    event RemoveAnchors(uint remoteChainId);

    modifier onlyAnchor(uint remoteChainId) {
        require(crossChains[remoteChainId].remoteChainId > 0,"remoteChainId not exist");
        require(crossChains[remoteChainId].anchors[msg.sender].remoteChainId == remoteChainId,"not anchors");
        _;
    }

    modifier onlyOwner() {
        require(msg.sender == owner,"not owner");
        _;
    }

    constructor() public {
        owner = msg.sender;
    }

    //登记链信息 管理员操作
    function chainRegister(uint remoteChainId, uint8 signConfirmCount, address[] memory _anchors) public onlyOwner returns(bool) {
        require (crossChains[remoteChainId].remoteChainId == 0,"remoteChainId already exist");
        uint64 temp = 0;
        address[] memory newAnchors;

        //初始化信息
        crossChains[remoteChainId] = Chain({
            remoteChainId: remoteChainId,
            signConfirmCount: signConfirmCount,
            anchorsPositionBit: (temp - 1) >> (64 - _anchors.length),
            anchorAddress:newAnchors
            });

        //加入锚定矿工
        for (uint8 i=0; i<_anchors.length; i++) {
            if (crossChains[remoteChainId].anchors[_anchors[i]].remoteChainId != 0) {
                revert();
            }
            crossChains[remoteChainId].anchorAddress.push(_anchors[i]);
            crossChains[remoteChainId].anchors[_anchors[i]] = Anchor({remoteChainId:remoteChainId,position:i});
        }

        return true;
    }

    //增加锚定矿工，管理员操作
    // position [0, 255]
    function addAnchors(uint remoteChainId, address[] memory _anchors) public onlyOwner {
        require (crossChains[remoteChainId].remoteChainId > 0,"remoteChainId not exist");
        require (_anchors.length > 0,"need _anchors");
        uint64 temp = 0;
        uint8 bitLen = uint8(bitCount(crossChains[remoteChainId].anchorsPositionBit));
        crossChains[remoteChainId].anchorsPositionBit = (temp - 1) >> (64 - bitLen - _anchors.length);

        //加入锚定矿工
        for (uint8 i=0; i<_anchors.length; i++) {
            if (crossChains[remoteChainId].anchors[_anchors[i]].remoteChainId != 0) {
                revert();
            }

            crossChains[remoteChainId].anchorAddress.push(_anchors[i]);
            crossChains[remoteChainId].anchors[_anchors[i]] = Anchor({remoteChainId:remoteChainId, position:i+bitLen});
        }

        emit AddAnchors(remoteChainId);
    }

    //移除锚定矿工, 管理员操作
    function removeAnchors(uint remoteChainId, address[] memory _anchors) public onlyOwner {
        require (crossChains[remoteChainId].remoteChainId > 0,"remoteChainId not exist");
        require (_anchors.length > 0,"need _anchors");
        uint8 bitLen = uint8(bitCount(crossChains[remoteChainId].anchorsPositionBit));
        require(bitLen - crossChains[remoteChainId].signConfirmCount >= _anchors.length,"_anchors too many");
        uint64 temp = 0;
        crossChains[remoteChainId].anchorsPositionBit = (temp - 1) >> (64 - bitLen + _anchors.length);

        for (uint8 i=0; i<_anchors.length; i++) {
            if (crossChains[remoteChainId].anchors[_anchors[i]].remoteChainId == 0) {
                revert();
            }

            uint8 index = crossChains[remoteChainId].anchors[_anchors[i]].position;
            if (index < crossChains[remoteChainId].anchorAddress.length - 1) {
                crossChains[remoteChainId].anchorAddress[index] = crossChains[remoteChainId].anchorAddress[crossChains[remoteChainId].anchorAddress.length - 1];
                crossChains[remoteChainId].anchors[crossChains[remoteChainId].anchorAddress[index]].position = index;
                crossChains[remoteChainId].anchorAddress.pop();
                delete crossChains[remoteChainId].anchors[_anchors[i]];
            } else {
                crossChains[remoteChainId].anchorAddress.pop();
                delete crossChains[remoteChainId].anchors[_anchors[i]];
            }
        }
        emit RemoveAnchors(remoteChainId);
    }

    function setSignConfirmCount(uint remoteChainId,uint8 count) public onlyOwner {
        require (crossChains[remoteChainId].remoteChainId > 0,"remoteChainId not exist");
        require (count != 0,"can not be zero");
        require (count <= crossChains[remoteChainId].anchorAddress.length,"not enough anchors");
        crossChains[remoteChainId].signConfirmCount = count;
    }

    function getMakerTx(bytes32 txId, uint remoteChainId) public view returns(uint){
        return crossChains[remoteChainId].makerTxs[txId];
    }

    function getTakerTx(bytes32 txId, uint remoteChainId) public view returns(uint){
        return crossChains[remoteChainId].takerTxs[txId];
    }

    function getAnchors(uint remoteChainId) public view returns(address[] memory,uint8){
        return (crossChains[remoteChainId].anchorAddress,crossChains[remoteChainId].signConfirmCount);
    }

    //增加跨链交易
    function makerStart(uint remoteChainId, uint destValue, bytes memory data) public payable {
        require(msg.value > 0,"value too low");
        require(crossChains[remoteChainId].remoteChainId > 0,"chainId not exist"); //是否支持的跨链
        bytes32 txId = keccak256(abi.encodePacked(msg.sender, list(), remoteChainId));
        assert(crossChains[remoteChainId].makerTxs[txId] == 0);
        crossChains[remoteChainId].makerTxs[txId] = msg.value;
        emit MakerTx(txId, msg.sender, remoteChainId, msg.value, destValue, data);
    }

    struct Recept {
        bytes32 txId;
        bytes32 txHash;
        address payable to;
        bytes32 blockHash;
        uint64 blockNumber;
        uint32 index;
        bytes  input;
        uint[] v;
        bytes32[] r;
        bytes32[] s;
    }
    //锚定节点执行
    function makerFinish(Recept memory rtx,uint remoteChainId,uint gasUsed) public onlyAnchor(remoteChainId) payable {
        require(rtx.v.length == rtx.r.length,"length error");
        require(rtx.v.length == rtx.s.length,"length error");
        require(rtx.to != address(0x0),"to is zero");
        uint256 value = crossChains[remoteChainId].makerTxs[rtx.txId];
        require(value > gasUsed,"not enough coin");
        require(verifySignAndCount(keccak256(abi.encodePacked(rtx.txId, rtx.txHash, rtx.to, rtx.blockHash, chainId() ,rtx.blockNumber,rtx.index,rtx.input)), remoteChainId, rtx.v, rtx.r, rtx.s) >= crossChains[remoteChainId].signConfirmCount,"sign error"); //签名数量
        delete crossChains[remoteChainId].makerTxs[rtx.txId];
        msg.sender.transfer(gasUsed);
        rtx.to.transfer(value - gasUsed);
        emit MakerFinish(rtx.txId,rtx.to);
    }

    function verifySignAndCount(bytes32 hash, uint remoteChainId, uint[] memory v, bytes32[] memory r, bytes32[] memory s) private view returns (uint8) {
        uint64 ret = 0;
        uint64 base = 1;
        for (uint i = 0; i < v.length; i++){
            v[i] -= remoteChainId*2;
            v[i] -= 8;
            address temp = ecrecover(hash, uint8(v[i]), r[i], s[i]);
            if (crossChains[remoteChainId].anchors[temp].remoteChainId == remoteChainId){
                ret = ret | (base << crossChains[remoteChainId].anchors[temp].position);
            }
        }
        return uint8(bitCount(ret));
    }

    function bitCount(uint64 n) public pure returns(uint64){
        uint64 tmp = n - ((n >>1) &0x36DB6DB6DB6DB6DB) - ((n >>2) &0x9249249249249249);
        return ((tmp + (tmp >>3)) &0x71C71C71C71C71C7) %63;
    }

    struct Order {
        uint value;
        bytes32 txId;
        bytes32 txHash;
        address payable from;
        bytes32 blockHash;
        uint destinationValue;
        bytes data;
        uint[] v;
        bytes32[] r;
        bytes32[] s;
    }

    function taker(Order memory ctx,uint remoteChainId,bytes memory input) payable public{
        require(ctx.v.length == ctx.r.length,"length error");
        require(ctx.v.length == ctx.s.length,"length error");
        require(msg.sender == ctx.from || msg.value >= ctx.destinationValue,"price wrong");
        require(crossChains[remoteChainId].takerTxs[ctx.txId] == 0,"txId exist");
        require(verifySignAndCount(keccak256(abi.encodePacked(ctx.value, ctx.txId, ctx.txHash, ctx.from, ctx.blockHash, chainId(), ctx.destinationValue,ctx.data)), remoteChainId,ctx.v,ctx.r,ctx.s) >= crossChains[remoteChainId].signConfirmCount,"sign error");
        crossChains[remoteChainId].takerTxs[ctx.txId] = ctx.value;
        ctx.from.transfer(msg.value);
        emit TakerTx(ctx.txId,msg.sender,remoteChainId,ctx.from,ctx.value,ctx.destinationValue,input);
    }

    function chainId() public pure returns (uint id) {
        assembly {
            id := chainid()
        }
    }

    function list() public pure returns (uint ll) {
        assembly {
            ll := nonce()
        }
    }
}
