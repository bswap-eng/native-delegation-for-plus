package config

import (
	"net/url"

	k2common "github.com/bswap-eng/native-delegation-for-plus/common"
	"github.com/ethereum/go-ethereum/common"
)

type K2Config struct {
	LoggerLevel                     string
	ValidatorWallets                []k2common.ValidatorWallet
	Web3SignerUrl                   *url.URL
	SignatureSwapperUrl             *url.URL
	BeaconNodeUrl                   *url.URL
	ExecutionNodeUrl                *url.URL
	K2LendingContractAddress        common.Address
	K2NodeOperatorContractAddress   common.Address
	ProposerRegistryContractAddress common.Address
	BalanceVerificationUrl          *url.URL       // for effective balance reporting for verifiable signatures to claim rewards
	SubgraphUrl                     *url.URL       // for querying the subgraph for validator registration status
	PayoutRecipient                 common.Address // to override the payout recipient for all validators
	ExclusionListFile               string         // to exclude validators from registration or native delegation
	StrictInclusionListFile         string         // to include only specified validators in registration or native delegation
	RepresentativeMappingFile       string         // to map fee recipients / specific validators to representatives
	MaxGasPrice                     uint64
	RegistrationOnly                bool
	ListenAddress                   *url.URL
	ClaimThreshold                  float64 // To only claim rewards if the validator has earned more than this threshold (in KETH)
}

var K2ConfigDefaults = K2Config{
	LoggerLevel:                     "info",
	ValidatorWallets:                nil,
	Web3SignerUrl:                   nil,
	SignatureSwapperUrl:             nil,
	BeaconNodeUrl:                   nil,
	ExecutionNodeUrl:                nil,
	K2LendingContractAddress:        common.Address{},
	K2NodeOperatorContractAddress:   common.Address{},
	ProposerRegistryContractAddress: common.Address{},
	BalanceVerificationUrl:          nil,
	SubgraphUrl:                     nil,
	PayoutRecipient:                 common.Address{},
	ExclusionListFile:               "",
	StrictInclusionListFile:         "",
	RepresentativeMappingFile:       "",
	MaxGasPrice:                     0,
	RegistrationOnly:                false,
	ListenAddress:                   &url.URL{Scheme: "http", Host: "localhost:10000"},
	ClaimThreshold:                  0.0,
}
