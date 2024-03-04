package k2

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"strconv"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"

	k2common "github.com/bswap-eng/native-delegation-for-plus/common"
	eth1Common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/bswap-eng/native-delegation-for-plus/config"
	"github.com/pon-network/mev-plus/common"
)

func (k2 *K2Service) parseConfig(moduleFlags common.ModuleFlags) (err error) {
	for flagName, flagValue := range moduleFlags {
		switch flagName {
		case config.LoggerLevelFlag.Name:
			logLevel, err := logrus.ParseLevel(flagValue)
			if err != nil {
				return err
			}
			k2.log.Logger.SetLevel(logLevel)
			k2.cfg.LoggerLevel = flagValue
		case config.WalletPrivateKeyFlag.Name:
			privateKeyStrs := strings.Split(flagValue, ",")

			for _, privateKeyStr := range privateKeyStrs {

				if privateKeyStr == "" {
					continue
				}

				pk, err := crypto.HexToECDSA(strings.TrimPrefix(privateKeyStr, "0x"))
				if err != nil {
					return fmt.Errorf("-%s: invalid wallet private private key %q", config.WalletPrivateKeyFlag.Name, privateKeyStr)
				}
				publicKey := pk.Public()
				walletAddress := crypto.PubkeyToAddress(*publicKey.(*ecdsa.PublicKey))

				k2.cfg.ValidatorWallets = append(k2.cfg.ValidatorWallets, k2common.ValidatorWallet{
					PrivateKey: pk,
					Address:    walletAddress,
				})
			}

		case config.Web3SignerUrlFlag.Name:
			k2.cfg.Web3SignerUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.Web3SignerUrlFlag.Name, flagValue)
			}
		case config.BeaconNodeUrlFlag.Name:
			k2.cfg.BeaconNodeUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.BeaconNodeUrlFlag.Name, flagValue)
			}
		case config.ExecutionNodeUrlFlag.Name:
			k2.cfg.ExecutionNodeUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.ExecutionNodeUrlFlag.Name, flagValue)
			}
		case config.PayoutRecipientFlag.Name:
			k2.cfg.PayoutRecipient = eth1Common.HexToAddress(flagValue)
			if k2.cfg.PayoutRecipient == (eth1Common.Address{}) {
				return fmt.Errorf("-%s: invalid address %q", config.PayoutRecipientFlag.Name, flagValue)
			}
		case config.ExclusionListFlag.Name:
			k2.cfg.ExclusionListFile = flagValue
		case config.StrictInclusionListFileFlag.Name:
			k2.cfg.StrictInclusionListFile = flagValue
		case config.RepresentativeMappingFlag.Name:
			k2.cfg.RepresentativeMappingFile = flagValue
		case config.MaxGasPriceFlag.Name:
			setMaxGasPrice, err := strconv.ParseUint(flagValue, 10, 64)
			if err != nil {
				return fmt.Errorf("-%s: invalid max gas price %q", config.MaxGasPriceFlag.Name, flagValue)
			}
			if setMaxGasPrice <= 0 {
				return fmt.Errorf("-%s: max gas price must be greater than zero", config.MaxGasPriceFlag.Name)
			}
			k2.cfg.MaxGasPrice = setMaxGasPrice
		case config.RegistrationOnlyFlag.Name:
			k2.cfg.RegistrationOnly, err = strconv.ParseBool(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid registration only flag %q", config.RegistrationOnlyFlag.Name, flagValue)
			}
		case config.ListenAddressFlag.Name:
			k2.cfg.ListenAddress, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.ListenAddressFlag.Name, flagValue)
			}
		case config.ClaimThresholdFlag.Name:
			k2.cfg.ClaimThreshold, err = strconv.ParseFloat(flagValue, 64)
			if err != nil {
				return fmt.Errorf("-%s: invalid claim threshold KETH amount %q", config.ClaimThresholdFlag.Name, flagValue)
			}
			// ensure the claim threshold is positive
			if k2.cfg.ClaimThreshold < 0 {
				return fmt.Errorf("-%s: claim threshold KETH amount must be positive", config.ClaimThresholdFlag.Name)
			}
		case config.K2LendingContractAddressFlag.Name:
			k2.cfg.K2LendingContractAddress = eth1Common.HexToAddress(flagValue)
			if k2.cfg.K2LendingContractAddress == (eth1Common.Address{}) {
				return fmt.Errorf("-%s: invalid address %q", config.K2LendingContractAddressFlag.Name, flagValue)
			}
		case config.K2NodeOperatorContractAddressFlag.Name:
			k2.cfg.K2NodeOperatorContractAddress = eth1Common.HexToAddress(flagValue)
			if k2.cfg.K2NodeOperatorContractAddress == (eth1Common.Address{}) {
				return fmt.Errorf("-%s: invalid address %q", config.K2NodeOperatorContractAddressFlag.Name, flagValue)
			}
		case config.ProposerRegistryContractAddressFlag.Name:
			k2.cfg.ProposerRegistryContractAddress = eth1Common.HexToAddress(flagValue)
			if k2.cfg.ProposerRegistryContractAddress == (eth1Common.Address{}) {
				return fmt.Errorf("-%s: invalid address %q", config.ProposerRegistryContractAddressFlag.Name, flagValue)
			}
		case config.SignatureSwapperUrlFlag.Name:
			k2.cfg.SignatureSwapperUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.SignatureSwapperUrlFlag.Name, flagValue)
			}
		case config.BalanceVerificationUrlFlag.Name:
			k2.cfg.BalanceVerificationUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.BalanceVerificationUrlFlag.Name, flagValue)
			}
		case config.SubgraphUrlFlag.Name:
			k2.cfg.SubgraphUrl, err = k2common.CreateUrl(flagValue)
			if err != nil {
				return fmt.Errorf("-%s: invalid url %q", config.SubgraphUrlFlag.Name, flagValue)
			}
		default:
			return fmt.Errorf("unknown flag %q", flagName)
		}

	}

	if len(moduleFlags) > 0 {
		k2.lock.Lock()
		k2.configured = true
		k2.lock.Unlock()
	}

	err = k2.checkConfig()
	if err != nil {
		return err
	}

	return nil
}

func (k2 *K2Service) checkConfig() error {

	k2.lock.Lock()
	defer k2.lock.Unlock()

	if !k2.configured {
		// If not set to run, return
		return nil
	}

	k2.configured = false

	// check that the execution node url is set
	if k2.cfg.ExecutionNodeUrl == nil {
		return fmt.Errorf("-%s: execution node url is required", config.ExecutionNodeUrlFlag.Name)
	}

	// check that the beacon node url is set
	if k2.cfg.BeaconNodeUrl == nil {
		return fmt.Errorf("-%s: beacon node url is required", config.BeaconNodeUrlFlag.Name)
	}

	// check that the wallet private key is set
	if len(k2.cfg.ValidatorWallets) == 0 {
		return fmt.Errorf("-%s: a validator wallet private key is required", config.WalletPrivateKeyFlag.Name)
	}

	// check that the web3 signer url is set
	if k2.cfg.Web3SignerUrl == nil && k2.cfg.PayoutRecipient != (eth1Common.Address{}) {
		return fmt.Errorf("-%s: web3 signer url is required in order to use a custom payout recepient", config.Web3SignerUrlFlag.Name)
	}

	// check if exclusion list file is set
	if k2.cfg.ExclusionListFile != "" {
		err := k2.readExclusionList(k2.cfg.ExclusionListFile)
		if err != nil {
			return err
		}
	}

	// check if strict list file is set
	if k2.cfg.StrictInclusionListFile != "" {
		err := k2.readInclusionList(k2.cfg.StrictInclusionListFile)
		if err != nil {
			return err
		}
	}

	// check if representative mapping file is set
	if k2.cfg.RepresentativeMappingFile != "" {
		err := k2.readRepresentativeMapping(k2.cfg.RepresentativeMappingFile)
		if err != nil {
			return err
		}
	}

	k2.configured = true

	return nil
}

func (k2 *K2Service) readExclusionList(filePath string) error {

	// Read the exclusion list file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open exclusion list file: %w", err)
	}
	defer file.Close()
	fileContent, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read exclusion list file: %w", err)
	}

	var exclusionList []k2common.ValidatorFilter
	err = json.Unmarshal(fileContent, &exclusionList)
	if err != nil {
		return fmt.Errorf("failed to parse exclusion list file: %w", err)
	}

	preparedExclusionList := make(map[string]k2common.ValidatorFilter)

	// Store the exclusion list
	k2.lock.Lock()
	defer k2.lock.Unlock()
	for _, entry := range exclusionList {
		// check if both a PublicKey and FeeRecipient are specified
		if entry.PublicKey != (phase0.BLSPubKey{}) && entry.FeeRecipient != (eth1Common.Address{}) {
			return fmt.Errorf("invalid exclusion list entry [%s, %s], cannot specify both PublicKey and FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}

		if entry.PublicKey == (phase0.BLSPubKey{}) && entry.FeeRecipient == (eth1Common.Address{}) {
			return fmt.Errorf("invalid exclusion list entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}

		if entry.ProposerRegistration && entry.NativeDelegation {
			entryForText := "validator"
			entryFor := entry.PublicKey.String()
			if entry.FeeRecipient != (eth1Common.Address{}) {
				entryForText = "fee recipient"
				entryFor = entry.FeeRecipient.String()
			}

			return fmt.Errorf("invalid exclusion list entry for %s, cannot exclude %s, as it has been set to be allowed for both proposer registration and native delegation", entryForText, entryFor)
		}

		if entry.PublicKey != (phase0.BLSPubKey{}) {
			// check if validator is already in the exclusion list
			if _, ok := preparedExclusionList[strings.ToLower(entry.PublicKey.String())]; ok {
				return fmt.Errorf("duplicate validator %s in exclusion list", entry.PublicKey.String())
			}
			// check if validator is in the inclusion list
			if _, ok := k2.strictInclusionList[strings.ToLower(entry.PublicKey.String())]; ok {
				return fmt.Errorf("validator %s is in both exclusion and inclusion list", entry.PublicKey.String())
			}
			preparedExclusionList[strings.ToLower(entry.PublicKey.String())] = entry
		} else if entry.FeeRecipient != (eth1Common.Address{}) {
			// check if fee recipient is already in the exclusion list
			if _, ok := preparedExclusionList[strings.ToLower(entry.FeeRecipient.String())]; ok {
				return fmt.Errorf("duplicate fee recipient %s in exclusion list", entry.FeeRecipient.String())
			}
			// check if fee recipient is in the inclusion list
			if _, ok := k2.strictInclusionList[strings.ToLower(entry.FeeRecipient.String())]; ok {
				return fmt.Errorf("fee recipient %s is in both exclusion and inclusion list", entry.FeeRecipient.String())
			}
			preparedExclusionList[strings.ToLower(entry.FeeRecipient.String())] = entry
		} else {
			// should never reach here but to ensure we do not proceed past an invalid entry in the file
			return fmt.Errorf("invalid exclusion list entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}
	}

	k2.exclusionList = preparedExclusionList

	if len(k2.exclusionList) > 0 {
		k2.log.Infof("Exclusion list updated with %d filters", len(k2.exclusionList))
	}

	return nil
}

func (k2 *K2Service) clearExclusionList() error {
	k2.lock.Lock()
	defer k2.lock.Unlock()
	k2.exclusionList = make(map[string]k2common.ValidatorFilter)
	return nil
}

func (k2 *K2Service) readInclusionList(filePath string) error {

	// Read the inclusion list file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open inclusion list file: %w", err)
	}
	defer file.Close()
	fileContent, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read inclusion list file: %w", err)
	}

	var inclusionList []k2common.ValidatorFilter
	err = json.Unmarshal(fileContent, &inclusionList)
	if err != nil {
		return fmt.Errorf("failed to parse inclusion list file: %w", err)
	}

	preparedInclusionList := make(map[string]k2common.ValidatorFilter)

	// Store the inclusion list
	k2.lock.Lock()
	defer k2.lock.Unlock()
	for _, entry := range inclusionList {
		// check if both a PublicKey and FeeRecipient are specified
		if entry.PublicKey != (phase0.BLSPubKey{}) && entry.FeeRecipient != (eth1Common.Address{}) {
			return fmt.Errorf("invalid inclusion list entry [%s, %s], cannot specify both PublicKey and FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}

		if entry.PublicKey == (phase0.BLSPubKey{}) && entry.FeeRecipient == (eth1Common.Address{}) {
			return fmt.Errorf("invalid inclusion list entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}

		if !entry.ProposerRegistration && !entry.NativeDelegation {
			entryForText := "validator"
			entryFor := entry.PublicKey.String()
			if entry.FeeRecipient != (eth1Common.Address{}) {
				entryForText = "fee recipient"
				entryFor = entry.FeeRecipient.String()
			}

			return fmt.Errorf("invalid inclusion list entry for %s, cannot include %s, as it has been set to be to not process both proposer registration and native delegation", entryForText, entryFor)
		}

		if entry.PublicKey != (phase0.BLSPubKey{}) {
			// check if validator is already in the inclusion list
			if _, ok := preparedInclusionList[strings.ToLower(entry.PublicKey.String())]; ok {
				return fmt.Errorf("duplicate validator %s in inclusion list", entry.PublicKey.String())
			}
			// check if validator is in the exclusion list
			if _, ok := k2.exclusionList[strings.ToLower(entry.PublicKey.String())]; ok {
				return fmt.Errorf("validator %s is in both exclusion and inclusion list", entry.PublicKey.String())
			}
			preparedInclusionList[strings.ToLower(entry.PublicKey.String())] = entry
		} else if entry.FeeRecipient != (eth1Common.Address{}) {
			// check if fee recipient is already in the inclusion list
			if _, ok := preparedInclusionList[strings.ToLower(entry.FeeRecipient.String())]; ok {
				return fmt.Errorf("duplicate fee recipient %s in inclusion list", entry.FeeRecipient.String())
			}
			// check if fee recipient is in the exclusion list
			if _, ok := k2.exclusionList[strings.ToLower(entry.FeeRecipient.String())]; ok {
				return fmt.Errorf("fee recipient %s is in both exclusion and inclusion list", entry.FeeRecipient.String())
			}
			preparedInclusionList[strings.ToLower(entry.FeeRecipient.String())] = entry
		} else {
			// should never reach here but to ensure we do not proceed past an invalid entry in the file
			return fmt.Errorf("invalid exclusion list entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", entry.PublicKey.String(), entry.FeeRecipient.String())
		}
	}

	k2.strictInclusionList = preparedInclusionList

	if len(k2.strictInclusionList) > 0 {
		k2.log.Infof("Strict inclusion list updated with %d filters", len(k2.strictInclusionList))
	}

	return nil
}

func (k2 *K2Service) clearInclusionList() error {
	k2.lock.Lock()
	defer k2.lock.Unlock()
	k2.strictInclusionList = make(map[string]k2common.ValidatorFilter)
	return nil
}

func (k2 *K2Service) readRepresentativeMapping(filePath string) error {
	// Read the representative mapping file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open representative mapping file: %w", err)
	}
	defer file.Close()
	fileContent, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read representative mapping file: %w", err)
	}

	var representativeMappingList []k2common.CustomPayoutRepresentative
	err = json.Unmarshal(fileContent, &representativeMappingList)
	if err != nil {
		return fmt.Errorf("failed to parse exclusion list file: %w", err)
	}

	preparedRepresentativeMapping := make(map[string]eth1Common.Address)
	trackRepresentativeMapping := make(map[string]eth1Common.Address)

	// Store the representative mapping
	k2.lock.Lock()
	defer k2.lock.Unlock()
	for _, representativeMapping := range representativeMappingList {
		if representativeMapping.RepresentativeAddress == (eth1Common.Address{}) {
			return fmt.Errorf("invalid representative address %s in representative mapping", representativeMapping.RepresentativeAddress.String())
		} else {
			// check if representative address is in configured wallets
			found := false
			for _, wallet := range k2.cfg.ValidatorWallets {
				if wallet.Address == representativeMapping.RepresentativeAddress {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("representative address %s in representative mapping is not a configured wallet", representativeMapping.RepresentativeAddress.String())
			}
		}

		if representativeMapping.FeeRecipientAddress == (eth1Common.Address{}) && representativeMapping.PublicKey == (phase0.BLSPubKey{}) {
			return fmt.Errorf("invalid representative mapping entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", representativeMapping.PublicKey.String(), representativeMapping.FeeRecipientAddress.String())
		}

		if representativeMapping.FeeRecipientAddress != (eth1Common.Address{}) && representativeMapping.PublicKey != (phase0.BLSPubKey{}) {
			return fmt.Errorf("invalid representative mapping entry [%s, %s], cannot specify both PublicKey and FeeRecipient in a single entry", representativeMapping.PublicKey.String(), representativeMapping.FeeRecipientAddress.String())
		}

		if representativeMapping.FeeRecipientAddress != (eth1Common.Address{}) {
			if _, ok := preparedRepresentativeMapping[strings.ToLower(representativeMapping.FeeRecipientAddress.String())]; ok {
				return fmt.Errorf("duplicate fee recipient %s in representative mapping", representativeMapping.FeeRecipientAddress.String())
			}
			if feeRecipient, ok := trackRepresentativeMapping[strings.ToLower(representativeMapping.RepresentativeAddress.String())]; ok {
				return fmt.Errorf("this representative address %s is already specified for fee recipient %s, cannot use it for another fee recipient %s", representativeMapping.RepresentativeAddress.String(), feeRecipient.String(), representativeMapping.FeeRecipientAddress.String())
			}
			preparedRepresentativeMapping[strings.ToLower(representativeMapping.FeeRecipientAddress.String())] = representativeMapping.RepresentativeAddress
			trackRepresentativeMapping[strings.ToLower(representativeMapping.RepresentativeAddress.String())] = representativeMapping.FeeRecipientAddress
		} else if representativeMapping.PublicKey != (phase0.BLSPubKey{}) {
			if _, ok := preparedRepresentativeMapping[strings.ToLower(representativeMapping.PublicKey.String())]; ok {
				return fmt.Errorf("duplicate validator %s in representative mapping", representativeMapping.PublicKey.String())
			}
			preparedRepresentativeMapping[strings.ToLower(representativeMapping.PublicKey.String())] = representativeMapping.RepresentativeAddress
		} else {
			// should never reach here but to ensure we do not proceed past an invalid entry in the file
			return fmt.Errorf("invalid representative mapping entry [%s, %s], must specify either PublicKey or FeeRecipient in a single entry", representativeMapping.PublicKey.String(), representativeMapping.FeeRecipientAddress.String())
		}

	}

	k2.representativeMapping = preparedRepresentativeMapping

	if len(k2.representativeMapping) > 0 {
		k2.log.Infof("Representative mapping updated with %d filters", len(k2.representativeMapping))
	}

	return nil
}

func (k2 *K2Service) clearRepresentativeMapping() error {
	k2.lock.Lock()
	defer k2.lock.Unlock()
	k2.representativeMapping = make(map[string]eth1Common.Address)
	return nil
}

func (k2 *K2Service) watchFile(label string, filePath string, readCallback func(string) error, clearCallback func() error) error {

	// Watch the file for changes use the k2.done channel to stop the watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create %s file watcher: %w", label, err)
	}

	// check if the file exists
	_, err = os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			k2.log.Infof("%s file %s does not exist, not watching", label, filePath)
			watcher.Close()
			return fmt.Errorf("%s file %s does not exist: %w", label, filePath, err)
		}
		watcher.Close()
		return fmt.Errorf("failed to stat %s file %s: %w", label, filePath, err)
	}

	// get the parent directory of the file
	fileDir := filepath.Dir(filePath)

	err = watcher.Add(fileDir)
	if err != nil {
		watcher.Close()
		return fmt.Errorf("failed to add %s file watcher: %w", label, err)
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					k2.log.Debugf("%s file watcher stopped", label)
					return
				}
				if (event.Op.Has(fsnotify.Write) || event.Op.Has(fsnotify.Create)) && event.Name == filePath {
					err := readCallback(filePath)
					if err != nil {
						k2.log.WithError(err).Warnf("Failed to read %s with provided callback", label)
					}
				} else if (event.Op.Has(fsnotify.Remove) || event.Op.Has(fsnotify.Rename)) && event.Name == filePath {
					// check if the file was removed
					if _, err := os.Stat(filePath); !os.IsNotExist(err) {
						// file still exists, not removing exclusion list
						continue
					}
					k2.log.Infof("%s file %s was renamed/removed.", label, filePath)
					err := clearCallback()
					if err != nil {
						k2.log.WithError(err).Warnf("Failed to clear %s with provided callback", label)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					k2.log.Debugf("%s file watcher stopped", label)
					return
				}
				k2.log.WithError(err).Errorf("Error watching %s file", label)
			case <-k2.exit:
				k2.log.Debugf("Stopping %s file watcher", label)
				return
			}
		}
	}()

	return nil
}
