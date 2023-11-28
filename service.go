package k2

import (
	"fmt"
	"sync"

	"github.com/restaking-cloud/native-delegation-for-plus/config"

	"github.com/restaking-cloud/native-delegation-for-plus/ethservice"
	ethConfig "github.com/restaking-cloud/native-delegation-for-plus/ethservice/config"

	"github.com/pon-network/mev-plus/common"
	coreCommon "github.com/pon-network/mev-plus/core/common"
	"github.com/restaking-cloud/native-delegation-for-plus/beacon"
	beaconConfig "github.com/restaking-cloud/native-delegation-for-plus/beacon/config"
	k2common "github.com/restaking-cloud/native-delegation-for-plus/common"
	"github.com/restaking-cloud/native-delegation-for-plus/signatureswapper"
	"github.com/restaking-cloud/native-delegation-for-plus/web3signer"

	apiv1 "github.com/attestantio/go-builder-client/api/v1"

	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"
)

type K2Service struct {
	coreClient       *coreCommon.Client
	log              *logrus.Entry
	signatureSwapper *signatureswapper.SignatureSwapperService
	web3Signer       *web3signer.Web3SignerService
	eth1             *ethservice.EthService
	beacon           *beacon.BeaconService
	lock             sync.Mutex

	cfg config.K2Config
}

func NewK2Service() *K2Service {
	return &K2Service{
		log:              logrus.NewEntry(logrus.New()).WithField("moduleExecution", config.ModuleName),
		signatureSwapper: signatureswapper.NewSignatureSwapperService(),
		web3Signer:       web3signer.NewWeb3SignerService(),
		eth1:             ethservice.NewEthService(),
		beacon:           beacon.NewBeaconService(),
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

	if k2.cfg.ValidatorWalletPrivateKey == nil {
		// module not configured to run
		return nil
	}

	k2.log.WithField("representativeAddress", k2.cfg.ValidatorWalletAddress).Info("Started K2 module")

	return nil
}

func (k2 *K2Service) Stop() error {
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
		return fmt.Errorf("chain id %v is not supported", chainId)
	}
	// beacon node chain id is supported, set the rest of the config
	k2.cfg.K2ContractAddress = knownConfig.K2ContractAddress
	k2.cfg.ProposerRegistryContractAddress = knownConfig.ProposerRegistryContractAddress
	k2.cfg.SignatureSwapperUrl = knownConfig.SignatureSwapperUrl

	// connect to the execution node and get the chain id, and contracts configured
	err = k2.eth1.Configure(ethConfig.EthServiceConfig{
		ExecutionNodeUrl:                k2.cfg.ExecutionNodeUrl,
		K2ContractAddress:               k2.cfg.K2ContractAddress,
		ProposerRegistryContractAddress: k2.cfg.ProposerRegistryContractAddress,
		ValidatorWalletPrivateKey:       k2.cfg.ValidatorWalletPrivateKey,
		ValidatorWalletAddress:          k2.cfg.ValidatorWalletAddress,
	})
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
		_, err = k2.web3Signer.Status()
		if err != nil {
			return fmt.Errorf("web3 signer is down: %v", err)
		}
	}

	return nil

}

func (k2 *K2Service) RegisterValidator(payload []apiv1.SignedValidatorRegistration) ([]k2common.K2ValidatorRegistration, error) {

	if k2.cfg.ValidatorWalletPrivateKey == nil {
		// module not configured to run
		return nil, nil
	}

	var proposers []string
	for _, reg := range payload {
		proposers = append(proposers, reg.Message.Pubkey.String())
	}

	return k2.batchProcessValidatorRegistrations(payload)
}
