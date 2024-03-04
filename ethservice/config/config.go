package config

import (
	"math/big"
	"net/url"

	k2common "github.com/bswap-eng/native-delegation-for-plus/common"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

type EthServiceConfig struct {
	ExecutionNodeUrl *url.URL
	ChainID          *big.Int

	MaxGasPrice *big.Int

	K2LendingContractAddress        common.Address
	K2NodeOperatorContractAddress   common.Address
	ProposerRegistryContractAddress common.Address

	// ABI
	K2LendingContractABI        *abi.ABI
	K2NodeOperatorContractABI   *abi.ABI
	ProposerRegistryContractABI *abi.ABI

	// Multicall
	MulticallContractAddress common.Address
	MulticallContractABI     *abi.ABI

	ValidatorWallets []k2common.ValidatorWallet
}
