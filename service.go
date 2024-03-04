package k2

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	balanceverifier "github.com/bswap-eng/native-delegation-for-plus/balanceverifier"
	"github.com/bswap-eng/native-delegation-for-plus/config"

	"github.com/bswap-eng/native-delegation-for-plus/ethservice"
	ethConfig "github.com/bswap-eng/native-delegation-for-plus/ethservice/config"
	ethcommon "github.com/ethereum/go-ethereum/common"

	"github.com/bswap-eng/native-delegation-for-plus/beacon"
	beaconConfig "github.com/bswap-eng/native-delegation-for-plus/beacon/config"
	k2common "github.com/bswap-eng/native-delegation-for-plus/common"
	"github.com/bswap-eng/native-delegation-for-plus/signatureswapper"
	"github.com/bswap-eng/native-delegation-for-plus/subgraph"
	"github.com/bswap-eng/native-delegation-for-plus/web3signer"
	"github.com/pon-network/mev-plus/common"
	coreCommon "github.com/pon-network/mev-plus/core/common"

	apiv1 "github.com/attestantio/go-builder-client/api/v1"

	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"
)

type K2Service struct {
	coreClient       *coreCommon.Client
	log              *logrus.Entry
	signatureSwapper *signatureswapper.SignatureSwapperService
	subgraph         *subgraph.SubgraphService
	web3Signer       *web3signer.Web3SignerService
	eth1             *ethservice.EthService
	beacon           *beacon.BeaconService
	balanceverifier  *balanceverifier.BalanceVerifierService
	lock             sync.Mutex

	server *http.Server

	exclusionList         map[string]k2common.ValidatorFilter // [Validator pubKey / Fee recipient address] -> Validator filter
	strictInclusionList   map[string]k2common.ValidatorFilter // [Validator pubKey / Fee recipient address] -> Validator filter
	representativeMapping map[string]ethcommon.Address        // [Fee recipient address / Validator pubKey] -> Representative address
	// *NOTE* Keep/Access the keys of the above maps in lower case to avoid case sensitivity issues [mixed checksums, etc.]

	// Track the last most recent timestamp that was processed
	lastRegistrationMessageTimestamp time.Time

	exit chan struct{}

	configured bool
	cfg        config.K2Config
}

func NewK2Service() *K2Service {
	return &K2Service{
		log:                   logrus.NewEntry(logrus.New()).WithField("moduleExecution", config.ModuleName),
		signatureSwapper:      signatureswapper.NewSignatureSwapperService(),
		subgraph:              subgraph.NewSubgraphService(),
		web3Signer:            web3signer.NewWeb3SignerService(),
		eth1:                  ethservice.NewEthService(),
		beacon:                beacon.NewBeaconService(),
		balanceverifier:       balanceverifier.NewBalanceVerifierService(),
		exclusionList:         make(map[string]k2common.ValidatorFilter),
		strictInclusionList:   make(map[string]k2common.ValidatorFilter),
		representativeMapping: make(map[string]ethcommon.Address),
		exit:                  make(chan struct{}),
		cfg:                   config.K2ConfigDefaults,
	}
}

func NewCommand() *cli.Command {
	return config.NewCommand()
}

func (k2 *K2Service) Name() string {
	return config.ModuleName
}

func (k2 *K2Service) Start() error {

	err := k2.checkConfig()
	// if module configuration has been called and completed without error, this should pose no error
	if err != nil {
		return err
	}

	if !k2.configured {
		// module not configured to run
		return nil
	}

	// start monitoring the exclusion list file
	if k2.cfg.ExclusionListFile != "" {
		go k2.watchFile("exclusion list", k2.cfg.ExclusionListFile, k2.readExclusionList, k2.clearExclusionList)
	}

	// start monitoring the inclusion list file
	if k2.cfg.StrictInclusionListFile != "" {
		go k2.watchFile("strict inclusion list", k2.cfg.StrictInclusionListFile, k2.readInclusionList, k2.clearInclusionList)
	}

	// start monitoring the representative mapping file
	if k2.cfg.RepresentativeMappingFile != "" {
		go k2.watchFile("representative mapping", k2.cfg.RepresentativeMappingFile, k2.readRepresentativeMapping, k2.clearRepresentativeMapping)
	}

	registryEnabled := k2.cfg.ProposerRegistryContractAddress != ethcommon.Address{}
	k2Enabled := (k2.cfg.K2LendingContractAddress != ethcommon.Address{}) && (k2.cfg.K2NodeOperatorContractAddress != ethcommon.Address{})

	if k2.server != nil {
		return fmt.Errorf("K2 server already running")
	}

	// start the server
	k2.server = &http.Server{
		Addr:    k2.cfg.ListenAddress.Host,
		Handler: k2.getRouter(),
	}

	go k2.startServer()

	// start monitoring
	go k2.monitor()

	var addresses string
	var addressesField string = "representativeAddress"
	for i, wallet := range k2.cfg.ValidatorWallets {
		delimiter := ","
		if i == len(k2.cfg.ValidatorWallets)-1 {
			delimiter = ""
			if i > 0 {
				addressesField = "representativeAddresses"
			}
		}
		addresses += wallet.Address.String() + delimiter
	}

	k2.log.WithFields(logrus.Fields{
		addressesField:    addresses,
		"registryEnabled": registryEnabled,
		"k2Enabled":       k2Enabled,
	}).Info("Started K2 module")

	return nil
}

func (k2 *K2Service) monitor() error {

	ctx := context.Background()
	ctxWithCancel, cancel := context.WithCancel(ctx)
	var HeadChan chan beacon.HeadEventData = make(chan beacon.HeadEventData, 64)

	// For utility in knowing the current slot
	go k2.beacon.SubscribeToHeadEvents(ctxWithCancel, HeadChan)

	// For each headslot event check whether the current timestamp and the last processed timestamp are more than 2 epochs apart
	// if so then issue a warning in the logs that the module has received no registration events for more than 2 epochs
	// need to check node and mevPlus are configured correctly for the builder api

	var lastWarnedTimestamp time.Time

	for {
		select {
		case <-k2.exit:
			cancel()
			return nil
		case <-ctxWithCancel.Done():
			return nil
		case <-HeadChan:
			currentTime := time.Now()
			k2.lock.Lock()
			if k2.lastRegistrationMessageTimestamp.IsZero() {
				// no registration events received yet
				if lastWarnedTimestamp.IsZero() {
					lastWarnedTimestamp = currentTime
				} else if currentTime.Sub(lastWarnedTimestamp) > (2 * time.Duration(12*32) * time.Second) {
					k2.log.Warnf("No registration events received yet from your node for more than 2 epochs since mevPlus started")
					k2.log.Debug("Please check your node and mevPlus are configured correctly for the builder api")
					lastWarnedTimestamp = currentTime
				}
			} else if currentTime.Sub(k2.lastRegistrationMessageTimestamp) > (2 * time.Duration(12*32) * time.Second) {
				// Send warning message every 2 mins if there is a warning to be sent
				if currentTime.Sub(lastWarnedTimestamp) > (2 * time.Minute) {
					k2.log.Warnf("No registration events received for more than 2 epochs from your node, last processed timestamp: %v", k2.lastRegistrationMessageTimestamp)
					k2.log.Debug("Please check your node and mevPlus are configured correctly for the builder api")
					lastWarnedTimestamp = currentTime
				}
			}
			k2.lock.Unlock()
		}
	}

}

func (k2 *K2Service) Stop() error {

	// stop monitoring files
	if k2.cfg.ExclusionListFile != "" || k2.cfg.RepresentativeMappingFile != "" || k2.cfg.StrictInclusionListFile != "" {
		close(k2.exit)
	}

	// stop the server
	err := k2.stopServer()
	if err != nil {
		return err
	}

	k2.log.Info("Stopped K2 module")

	return nil
}

func (k2 *K2Service) ConnectCore(coreClient *coreCommon.Client, pingId string) error {

	// this is the first and only time the client is set and doesnt need a mutex
	k2.coreClient = coreClient

	// test a ping to the core server
	err := k2.coreClient.Ping(pingId)
	if err != nil {
		return err
	}

	return nil
}

func (k2 *K2Service) Configure(moduleFlags common.ModuleFlags) (err error) {

	err = k2.parseConfig(moduleFlags)
	if err != nil {
		return err
	}

	// connect to the beacon node and get the chain id configured
	err = k2.beacon.Configure(beaconConfig.BeaconConfig{
		BeaconNodeUrl: k2.cfg.BeaconNodeUrl,
	})
	if err != nil {
		return err
	}

	// retrieve the chain id from the beacon node
	chainId := k2.beacon.ConnectedChainId().Uint64()

	// check if chain id is supported
	knownConfig, ok := config.K2ConfigConstants[chainId]
	if !ok {

		// check if the module has provided contract addresses by the user
		if k2.cfg.RegistrationOnly {
			// If registration only and not supported check for provided ProposerRegistryContractAddress
			if k2.cfg.ProposerRegistryContractAddress != (ethcommon.Address{}) {
				k2.log.Debug("User provided ProposerRegistryContractAddress for registration only")
			} else {
				return fmt.Errorf("chain id %v is not supported", chainId)
			}
		} else if k2.cfg.K2LendingContractAddress != (ethcommon.Address{}) || k2.cfg.K2NodeOperatorContractAddress != (ethcommon.Address{}) {
			// If K2 enabled and not supported check for provided K2LendingContractAddress and K2NodeOperatorContractAddress
			// either contract addresses were provided by the user
			// require the need for both contract addresses to be provided
			if k2.cfg.K2LendingContractAddress == (ethcommon.Address{}) || k2.cfg.K2NodeOperatorContractAddress == (ethcommon.Address{}) {
				return fmt.Errorf("provide both K2LendingContractAddress and K2NodeOperatorContractAddress for chain id %v", chainId)
			}

			// Signature swapper and balance verifier are required for K2 operations
			// but no need to check if the user provided SignatureSwapperUrl and BalanceVerificationUrl here
			// as the individual services will check for the required configuration and throw an error if not provided

			// No need to further check if the user provided ProposerRegistryContractAddress in addition as this can be obtained from the k2 contracts
			k2.log.Debug("User provided K2LendingContractAddress and K2NodeOperatorContractAddress")
		} else {
			return fmt.Errorf("chain id %v is not supported", chainId)
		}
	} else {
		// beacon node chain id is supported, set the rest of the config and use any provided contract addresses or urls as overrides
		if k2.cfg.K2LendingContractAddress == (ethcommon.Address{}) {
			k2.cfg.K2LendingContractAddress = knownConfig.K2LendingContractAddress
		} else {
			k2.log.Debugf("User provided K2LendingContractAddress: %s", k2.cfg.K2LendingContractAddress.String())
		}

		if k2.cfg.K2NodeOperatorContractAddress == (ethcommon.Address{}) {
			k2.cfg.K2NodeOperatorContractAddress = knownConfig.K2NodeOperatorContractAddress
		} else {
			k2.log.Debugf("User provided K2NodeOperatorContractAddress: %s", k2.cfg.K2NodeOperatorContractAddress.String())
		}

		if k2.cfg.ProposerRegistryContractAddress == (ethcommon.Address{}) {
			k2.cfg.ProposerRegistryContractAddress = knownConfig.ProposerRegistryContractAddress
		} else {
			k2.log.Debugf("User provided ProposerRegistryContractAddress: %s", k2.cfg.ProposerRegistryContractAddress.String())
		}

		if k2.cfg.SignatureSwapperUrl == nil {
			k2.cfg.SignatureSwapperUrl = knownConfig.SignatureSwapperUrl
		} else {
			k2.log.Debugf("User provided SignatureSwapperUrl: %s", k2.cfg.SignatureSwapperUrl.String())
		}

		if k2.cfg.BalanceVerificationUrl == nil {
			k2.cfg.BalanceVerificationUrl = knownConfig.BalanceVerificationUrl
		} else {
			k2.log.Debugf("User provided BalanceVerificationUrl: %s", k2.cfg.BalanceVerificationUrl.String())
		}

		if k2.cfg.SubgraphUrl == nil {
			k2.cfg.SubgraphUrl = knownConfig.SubgraphUrl
		} else {
			k2.log.Debugf("User provided SubgraphUrl: %s", k2.cfg.SubgraphUrl.String())
		}

	}

	if k2.cfg.RegistrationOnly {
		// module configured to only register validators and not delegate
		k2.log.Debug("Module configured to only register validators and not delegate")
		k2.cfg.K2LendingContractAddress = ethcommon.Address{}
		k2.cfg.K2NodeOperatorContractAddress = ethcommon.Address{}
	}

	// connect to the execution node and get the chain id, and contracts configured
	err = k2.eth1.Configure(ethConfig.EthServiceConfig{
		ExecutionNodeUrl:                k2.cfg.ExecutionNodeUrl,
		K2LendingContractAddress:        k2.cfg.K2LendingContractAddress,
		K2NodeOperatorContractAddress:   k2.cfg.K2NodeOperatorContractAddress,
		ProposerRegistryContractAddress: k2.cfg.ProposerRegistryContractAddress,
		ValidatorWallets:                k2.cfg.ValidatorWallets,
	}, k2.log)
	if err != nil {
		return err
	}

	// Ensure that the chain id reported by the beacon node matches the chain id reported by the execution node
	eth1ChainId := k2.eth1.ConnectedChainId().Uint64()
	if eth1ChainId != chainId {
		// wrong chain id configured for the execution node, needs to match the beacon node (validator truth source)
		return fmt.Errorf("chain id mismatch: beacon node reports %v, execution node reports %v", chainId, eth1ChainId)
	}

	// configure and connect to off-chain signature tools
	if k2.cfg.Web3SignerUrl != nil {
		err = k2.web3Signer.Configure(k2.cfg.Web3SignerUrl)
		if err != nil {
			return err
		}
	}
	err = k2.signatureSwapper.Configure(k2.cfg.SignatureSwapperUrl)
	if err != nil {
		return err
	}
	// Ensure that the chain id reported by the beacon node matches the chain id reported by the signature swapper
	sigSwapperChainId := k2.signatureSwapper.ConnectedChainId().Uint64()
	if sigSwapperChainId != chainId {
		// wrong chain id configured for the signature swapper, needs to match the beacon node (validator truth source)
		return fmt.Errorf("chain id mismatch: beacon node reports %v, signature swapper reports %v", chainId, sigSwapperChainId)
	}

	// If configured for K2 operations
	if (k2.cfg.K2LendingContractAddress != ethcommon.Address{}) && (k2.cfg.K2NodeOperatorContractAddress != ethcommon.Address{}) {
		err = k2.balanceverifier.Configure(k2.cfg.BalanceVerificationUrl)
		if err != nil {
			return err
		}

		// Ensure that the chain id reported by the beacon node matches the chain id reported by the effective balance verifier
		balanceVerifierChainId := k2.balanceverifier.ConnectedChainId().Uint64()
		if balanceVerifierChainId != chainId {
			// wrong chain id configured for the effective balance verification service, needs to match the beacon node (validator truth source)
			return fmt.Errorf("chain id mismatch: beacon node reports %v, balance verifier reports %v", chainId, balanceVerifierChainId)
		}

		// Subgraph is not essential for operations, just to query delegated validators
		// only attempt to configure if exists and configurable
		if k2.cfg.SubgraphUrl != nil {
			err = k2.subgraph.Configure(k2.cfg.SubgraphUrl)
			if err != nil {
				return err
			}

			metaInfo, err := k2.subgraph.MetaInfo()
			if err != nil {
				return err
			}

			blockInfo, err := k2.eth1.GetBlock(big.NewInt(int64(metaInfo.Meta.Block.Number)))
			if err != nil {
				return err
			}

			// If blockhash from subgraph and blockhash from eth1 node match for the given latest subgraph block number
			if strings.EqualFold(metaInfo.Meta.Block.Hash, blockInfo.Hash().String()) {
				k2.log.WithFields(
					logrus.Fields{
						"blockNumber":            metaInfo.Meta.Block.Number,
						"blockHash":              metaInfo.Meta.Block.Hash,
						"deducedSubgraphChainId": eth1ChainId,
					},
				).Debug("Subgraph blockhash matches eth1 node blockhash for the latest subgraph block number")
				// then set the configured eth1 connected chain ID to the subgraph chain ID
				k2.subgraph.SetConnectedChainID(big.NewInt(int64(eth1ChainId)))
			} else {
				return fmt.Errorf("invalid subgraph: subgraph blockhash does not match eth1 node blockhash for the latest subgraph block number")
			}

		}
	}

	if k2.cfg.MaxGasPrice > 0 {
		// Then the user has set a max gas price, set it on the eth1 service
		k2.eth1.SetMaxGasPrice(k2.cfg.MaxGasPrice)
	}

	return nil
}

func (k2 *K2Service) Status() error {

	// check beacon node is up
	_, err := k2.beacon.Status()
	if err != nil {
		return fmt.Errorf("beacon node is down: %v", err)
	}

	// check execution node is up
	_, err = k2.eth1.Status()
	if err != nil {
		return fmt.Errorf("execution node is down: %v", err)
	}

	// check signature swapper is up
	_, err = k2.signatureSwapper.GetInfo()
	if err != nil {
		return fmt.Errorf("signature swapper is down: %v", err)
	}

	// check web3 signer is up if configured
	if k2.cfg.Web3SignerUrl != nil {
		err = k2.web3Signer.Status()
		if err != nil {
			return fmt.Errorf("web3 signer is down: %v", err)
		}
	}

	return nil

}

func (k2 *K2Service) RegisterValidator(payload []apiv1.SignedValidatorRegistration) ([]k2common.K2ValidatorRegistration, error) {

	if !k2.configured {
		// module not configured to run
		return nil, nil
	}

	var recentTimestamp time.Time
	var proposers []string
	for _, reg := range payload {
		proposers = append(proposers, reg.Message.Pubkey.String())
		if reg.Message.Timestamp.After(recentTimestamp) {
			recentTimestamp = reg.Message.Timestamp
		}
	}

	k2.log.Debugf("Registering validators: %v", proposers)

	k2.lock.Lock()
	if recentTimestamp.After(k2.lastRegistrationMessageTimestamp) {
		k2.lastRegistrationMessageTimestamp = recentTimestamp
	}
	k2.lock.Unlock()

	return k2.batchProcessValidatorRegistrations(payload)
}
