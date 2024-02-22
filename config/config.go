package config

import (
	"net/url"

	"github.com/ethereum/go-ethereum/common"
	k2common "github.com/restaking-cloud/native-delegation-for-plus/common"
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
	PayoutRecipient                 common.Address // to override the payout recipient for all validators
	ExclusionListFile               string         // to exclude validators from registration or native delegation
	RepresentativeMappingFile       string         // to map representative addresses to fee recipients that may be varied among validators
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
	PayoutRecipient:                 common.Address{},
	ExclusionListFile:               "",
	RepresentativeMappingFile:       "",
	MaxGasPrice:                     0,
	RegistrationOnly:                false,
	ListenAddress:                   &url.URL{Scheme: "http", Host: "localhost:10000"},
	ClaimThreshold:                  0.0,
}
