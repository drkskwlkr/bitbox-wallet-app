// Copyright 2018 Shift Devices AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package blockchain

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/digitalbitbox/bitbox-wallet-app/util/errp"
)

// TXHash wraps chainhash.Hash for json deserialization.
type TXHash chainhash.Hash

// Hash returns the wrapped hash.
func (txHash *TXHash) Hash() chainhash.Hash {
	return chainhash.Hash(*txHash)
}

// MarshalJSON implements the json.Marshaler interface.
func (txHash *TXHash) MarshalJSON() ([]byte, error) {
	return json.Marshal(txHash.Hash().String())
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (txHash *TXHash) UnmarshalJSON(jsonBytes []byte) error {
	var txHashStr string
	if err := json.Unmarshal(jsonBytes, &txHashStr); err != nil {
		return errp.WithStack(err)
	}
	t, err := chainhash.NewHashFromStr(txHashStr)
	if err != nil {
		return errp.WithStack(err)
	}
	*txHash = TXHash(*t)
	return nil
}

// TxInfo is returned by ScriptHashGetHistory.
type TxInfo struct {
	Height int    `json:"height"`
	TXHash TXHash `json:"tx_hash"`
	Fee    *int64 `json:"fee"`
}

// TxHistory is returned by ScriptHashGetHistory.
type TxHistory []*TxInfo

// Status encodes the status of the address history as a hash, according to the Electrum
// specification.
// https://github.com/kyuupichan/electrumx/blob/b01139bb93a7b0cfbd45b64e170223f4871a4a87/docs/PROTOCOL.rst#blockchainaddresssubscribe
func (history TxHistory) Status() string {
	if len(history) == 0 {
		return ""
	}
	status := bytes.Buffer{}
	for _, tx := range history {
		status.WriteString(fmt.Sprintf("%s:%d:", tx.TXHash.Hash().String(), tx.Height))
	}
	return hex.EncodeToString(chainhash.HashB(status.Bytes()))
}

// ScriptHashHex is the hash of a pkScript in reverse hex format.
type ScriptHashHex string

// Header is returned by HeadersSubscribe().
type Header struct {
	BlockHeight int `json:"block_height"`
}

// Status is the connection status to the blockchain node
type Status int

const (
	// CONNECTED indicates that we are online
	CONNECTED Status = iota
	// DISCONNECTED indicates that we are offline
	DISCONNECTED
)

// Interface is the interface to a blockchain index backend. Currently geared to Electrum, though
// other backends can implement the same interface.
//go:generate mockery -name Interface
type Interface interface {
	ScriptHashGetHistory(ScriptHashHex, func(TxHistory) error, func(error))
	TransactionGet(chainhash.Hash, func(*wire.MsgTx) error, func(error))
	ScriptHashSubscribe(func() func(error), ScriptHashHex, func(string) error)
	HeadersSubscribe(func() func(error), func(*Header) error)
	TransactionBroadcast(*wire.MsgTx) error
	RelayFee(func(btcutil.Amount), func(error))
	EstimateFee(int, func(*btcutil.Amount) error, func(error))
	Headers(int, int, func([]*wire.BlockHeader, int) error, func(error))
	GetMerkle(chainhash.Hash, int, func(merkle []TXHash, pos int) error, func(error))
	Close()
	ConnectionStatus() Status
	RegisterOnConnectionStatusChangedEvent(func(Status))
}
