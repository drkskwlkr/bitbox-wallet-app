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

package eth

import (
	"math/big"
	"strings"
	"sync"

	"github.com/digitalbitbox/bitbox-wallet-app/backend/coins/coin"
	"github.com/digitalbitbox/bitbox-wallet-app/backend/coins/eth/erc20"
	"github.com/digitalbitbox/bitbox-wallet-app/backend/coins/eth/etherscan"
	"github.com/digitalbitbox/bitbox-wallet-app/util/logging"
	"github.com/digitalbitbox/bitbox-wallet-app/util/observable"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/sirupsen/logrus"
)

// Coin models an Ethereum coin.
type Coin struct {
	observable.Implementation
	initOnce              sync.Once
	client                *ethclient.Client
	code                  string
	unit                  string
	feeUnit               string
	net                   *params.ChainConfig
	blockExplorerTxPrefix string
	nodeURL               string
	etherScanURL          string
	erc20Token            *erc20.Token
	etherScan             *etherscan.EtherScan

	log *logrus.Entry
}

// NewCoin creates a new coin with the given parameters.
// For erc20 tokens, provide erc20Token using NewERC20Token() (otherwise keep nil).
func NewCoin(
	code string,
	unit string,
	feeUnit string,
	net *params.ChainConfig,
	blockExplorerTxPrefix string,
	etherScanURL string,
	nodeURL string,
	erc20Token *erc20.Token,
) *Coin {
	return &Coin{
		code:                  code,
		unit:                  unit,
		feeUnit:               feeUnit,
		net:                   net,
		blockExplorerTxPrefix: blockExplorerTxPrefix,
		nodeURL:               nodeURL,
		etherScanURL:          etherScanURL,
		erc20Token:            erc20Token,

		log: logging.Get().WithGroup("coin").WithField("code", code),
	}
}

// Net returns the network (mainnet, testnet, etc.).
func (coin *Coin) Net() *params.ChainConfig { return coin.net }

// Initialize implements coin.Coin.
func (coin *Coin) Initialize() {
	coin.initOnce.Do(func() {
		coin.log.Infof("connecting to %s", coin.nodeURL)
		client, err := ethclient.Dial(coin.nodeURL)
		if err != nil {
			// TODO: init conn lazily, feed error via EventStatusChanged
			panic(err)
		}
		coin.client = client

		coin.etherScan = etherscan.NewEtherScan(coin.etherScanURL)
	})
}

// Code implements coin.Coin.
func (coin *Coin) Code() string {
	return coin.code
}

// Unit implements coin.Coin.
func (coin *Coin) Unit(isFee bool) string {
	if isFee {
		return coin.feeUnit
	}
	return coin.unit
}

// FormatAmount implements coin.Coin.
func (coin *Coin) FormatAmount(amount coin.Amount, isFee bool) string {
	var factor *big.Int
	if !isFee && coin.erc20Token != nil {
		// 10^decimals
		factor = new(big.Int).Exp(
			big.NewInt(10),
			new(big.Int).SetUint64(uint64(coin.erc20Token.Decimals())), nil)
	} else {
		// Standard Ethereum
		factor = big.NewInt(1e18)
	}
	return strings.TrimRight(strings.TrimRight(
		new(big.Rat).SetFrac(amount.BigInt(), factor).FloatString(18),
		"0"), ".")
}

// ToUnit implements coin.Coin.
func (coin *Coin) ToUnit(amount coin.Amount, isFee bool) float64 {
	ether := big.NewInt(1e18)
	result, _ := new(big.Rat).SetFrac(amount.BigInt(), ether).Float64()
	return result
}

// BlockExplorerTransactionURLPrefix implements coin.Coin.
func (coin *Coin) BlockExplorerTransactionURLPrefix() string {
	return coin.blockExplorerTxPrefix
}

// EtherScan returns an instance of EtherScan.
func (coin *Coin) EtherScan() *etherscan.EtherScan {
	return coin.etherScan
}

func (coin *Coin) String() string {
	return coin.code
}

// SmallestUnit implements coin.Coin.
func (coin *Coin) SmallestUnit() string {
	return "wei"
}
