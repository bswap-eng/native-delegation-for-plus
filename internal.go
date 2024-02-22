package k2

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"

	apiv1 "github.com/attestantio/go-builder-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
	k2common "github.com/restaking-cloud/native-delegation-for-plus/common"
	"github.com/sirupsen/logrus"
)

func (k2 *K2Service) processValidatorRegistrations(payload []apiv1.SignedValidatorRegistration) ([]k2common.K2ValidatorRegistration, error) {
	k2.lock.Lock()
	defer k2.lock.Unlock()

	if len(payload) == 0 {
		return nil, nil
	}

	// Default to using the primary representative address for the registrations
	var representative k2common.ValidatorWallet = k2.cfg.ValidatorWallets[0]
	var setPayoutRecipient common.Address = common.Address(payload[0].Message.FeeRecipient)
	var nodeOperatorTopayoutRecipientMapping map[string]common.Address = make(map[string]common.Address)

	var globalMaxNativeDelegation *big.Int = big.NewInt(0)
	var individualMaxNativeDelegation *big.Int = big.NewInt(0)

	var currentGlobalNativeDelegation *big.Int = big.NewInt(0)
	var currentIndividualNativeDelegation *big.Int = big.NewInt(0)

	var isInInclusionList bool

	var preChecksComplete atomic.Bool
	var preChecksError atomic.Value

	// Start go routines to get the global and current individual native delegation
	// if the currentGlobal is at the max then check the currentIndividual and maxIndividual
	go func() {
		if k2.cfg.K2LendingContractAddress != (common.Address{}) {
			// If the module is configured for K2 operations in addition to Proposer Registry operations
			// use a sync wait group to wait for the results of the global and individual native delegation checks
			var wg sync.WaitGroup
			wg.Add(3)

			go func() {
				defer wg.Done()
				var err error
				globalMaxNativeDelegation, err = k2.eth1.GlobalMaxNativeDelegation()
				if err != nil {
					k2.log.WithError(err).Error("failed to get global max native delegation")
					preChecksError.Store(err)
					return
				}
				k2.log.WithField("globalMaxNativeDelegation", globalMaxNativeDelegation.String()).Debug("global max native delegation")
			}()

			go func() {
				defer wg.Done()
				var err error
				currentGlobalNativeDelegation, err = k2.eth1.GetTotalNativeDelegationCapacityConsumed()
				if err != nil {
					k2.log.WithError(err).Error("failed to get current global native delegation")
					preChecksError.Store(err)
					return
				}
				k2.log.WithField("currentGlobalNativeDelegation", currentGlobalNativeDelegation.String()).Debug("current global native delegation")
			}()

			go func() {
				defer wg.Done()
				var err error
				configuredWalletAddresses := make([]common.Address, len(k2.cfg.ValidatorWallets))
				for i, wallet := range k2.cfg.ValidatorWallets {
					configuredWalletAddresses[i] = wallet.Address
				}
				nodeOperatorTopayoutRecipientMapping, err = k2.eth1.NodeOperatorToPayoutRecipient(configuredWalletAddresses)
				if err != nil {
					k2.log.WithError(err).Error("failed to get configured wallet addresses to payout recipient mapping")
					preChecksError.Store(err)
					return
				}
				k2.log.WithField("payoutRecipientCheck", nodeOperatorTopayoutRecipientMapping).Debug("configured wallet addresses to payout recipient mapping")
			}()

			wg.Wait()

			if preChecksError.Load() != nil {
				// error occurred while getting the global and current individual native delegation
				// so cannot proceed with the registrations
				k2.log.WithError(preChecksError.Load().(error)).Error("failed to get global and current individual native delegation")
				return
			}

			// if k2 operations then the right representative address to use for the registrations must be determined

			// can do this safely as batch processing would have grouped by feeRecipient and this is our initial
			// recipient until overwritten by the flag or by a fee recipient in the contracts
			payloadFeeRecipient := setPayoutRecipient
			var representativeFound bool
			// check if there is a strict representative address for the payload's fee recipient
			if useRepAddress, ok := k2.representativeMapping[payloadFeeRecipient.String()]; ok {
				for _, wallet := range k2.cfg.ValidatorWallets {
					if strings.EqualFold(wallet.Address.String(), useRepAddress.String()) {
						representative = wallet
						representativeFound = true
						break
					}
				}
				if !representativeFound {
					// representative not not configured in the module for the payload's fee recipient
					// throw an error since module cant strictly use this wallet
					k2.log.WithFields(logrus.Fields{
						"payloadFeeRecipient": payloadFeeRecipient.String(),
						"representative":      useRepAddress.String(),
					}).Error("strict representative address for payload's fee recipient not configured")
					preChecksError.Store(fmt.Errorf("strict representative address (%s) for payload's fee recipient (%s) not configured", useRepAddress.String(), payloadFeeRecipient.String()))
					return
				} else {
					// If representative configured, check if the representative has been used in the k2 lending contract
					// before, and if so is the fee recipient the same as the payload's fee recipient
					payoutRecipient := nodeOperatorTopayoutRecipientMapping[representative.Address.String()]
					if (payoutRecipient == common.Address{}) {
						// then representative is potentially unused and can be used for this payload's fee recipient
						// so use this representative address for the payload's fee recipient
						k2.log.WithFields(logrus.Fields{
							"payloadFeeRecipient":           payloadFeeRecipient.String(),
							"representative":                representative.Address.String(),
							"representativePayoutRecipient": payoutRecipient.String(),
						}).Debug("using strict representative address for payload's fee recipient as it is potentially unused")
						if k2.cfg.Web3SignerUrl != nil && k2.cfg.PayoutRecipient != (common.Address{}) {
							// if the k2 web3signer has been set and a payout recipient has been set,
							// then use that as the global payout recipient to overwrite the payload's fee recipient
							payloadFeeRecipient = k2.cfg.PayoutRecipient
							k2.log.WithField("payoutRecipient", payloadFeeRecipient.String()).Debug("using the payout recipient set in the configuration to overwrite the payload's fee recipient for K2")
						}

					} else if !strings.EqualFold(payoutRecipient.String(), payloadFeeRecipient.String()) {
						// wallet is used if here, and payout recipient in the k2 lending contracts for this wallet is not the same as the payload's fee recipient

						// the wallet fee recipient on-chain does not match the payload
						// if the k2 web3signer has been set and a payout recipient has been set,
						// check if this matches the payout recipient in the k2 lending contracts
						if k2.cfg.Web3SignerUrl != nil && k2.cfg.PayoutRecipient != (common.Address{}) && strings.EqualFold(payoutRecipient.String(), k2.cfg.PayoutRecipient.String()) {
							// if the global payout recipient has been set in the configuration
							// and it matches the payout recipient in the k2 lending contracts for this
							// strict use representative address then use the payout recipient set in the configuration
							// to overwrite the payload's fee recipient for K2, as this is the same as the payout recipient in the contracts
							// and the module has a web3signer set to make the change to the payload
							payloadFeeRecipient = k2.cfg.PayoutRecipient
							k2.log.WithField("payoutRecipient", payloadFeeRecipient.String()).Debug("using the payout recipient set in the configuration (same as payout for rep in contracts) to overwrite the payload's fee recipient for K2")
						} else if k2.cfg.Web3SignerUrl != nil && k2.cfg.PayoutRecipient != (common.Address{}) && !strings.EqualFold(payoutRecipient.String(), k2.cfg.PayoutRecipient.String()) {
							// if the payout recipient in the k2 lending contracts for this strict representative address
							// does not match the global payout recipient set in the configuration
							// then since the module is configured with a web3signer, substitute the payload's payout recipient with that set
							// in the contracts for this representative address
							// as we cannot change this again for this registration since its set in the contracts already for this wallet
							initialPayloadFeeRecipient := payloadFeeRecipient
							payloadFeeRecipient = payoutRecipient
							k2.log.WithFields(logrus.Fields{
								"intialPayloadFeeRecipient": initialPayloadFeeRecipient.String(),
								"payloadFeeRecipient":       payloadFeeRecipient.String(),
								"representative":            representative.Address.String(),
							}).Debug("using the payout recipient set in the k2 contract to overwrite the payload's fee recipient for K2")
						} else {
							// no web3signer set, and the payout recipient in the k2 lending contracts for this wallet
							// does not match the payload's fee recipient so throw an error, since we cannot modify the payload anyway
							// and cannot proceed with the registration either
							k2.log.WithFields(logrus.Fields{
								"payloadFeeRecipient":  payloadFeeRecipient.String(),
								"representative":       representative.Address.String(),
								"setK2PayoutRecipient": payoutRecipient.String(),
							}).Error("strict representative address set for payload's fee recipient does not match the set payout recipient in the contracts for this wallet")
							preChecksError.Store(fmt.Errorf("strict representative address (%s) for payload's fee recipient (%s) does not match the set payout recipient (%s) in the contracts for this representative", representative.Address.String(), payloadFeeRecipient.String(), payoutRecipient.String()))
							return
						}
					}

				}

				k2.log.WithFields(logrus.Fields{
					"payloadFeeRecipient": payloadFeeRecipient.String(),
					"representative":      representative.Address.String(),
				}).Debug("using strict representative address for payload's fee recipient")
			} else { // if no strict representative address for the payload's fee recipient then determine the right representative address to use
				var unusedRepresentative k2common.ValidatorWallet = k2common.ValidatorWallet{
					Address: common.Address{},
				} // holds the next available unused representative address configured
				var firstUsedRepresentative k2common.ValidatorWallet = k2.cfg.ValidatorWallets[0] // holds the first used representative address configured

				for _, wallet := range k2.cfg.ValidatorWallets { // Check wallets in order of priority set in the configuration
					payoutRecipient := nodeOperatorTopayoutRecipientMapping[wallet.Address.String()]
					if strings.EqualFold(payoutRecipient.String(), payloadFeeRecipient.String()) {
						representative = wallet // representative found for the set feeRecipient continue further native delegation using this representative as the payload fee recipient matches
						representativeFound = true
						break
					} else if (payoutRecipient == common.Address{}) && (unusedRepresentative.Address == common.Address{}) {
						// representative not found for the feeRecipient check if the wallet means its potentially unused
						unusedRepresentative = wallet
					}
				}
				if !representativeFound {
					// Representative not found in contract for this payload's payout recipient
					// check if there is an avaialable representative that has not been used
					if unusedRepresentative.Address == (common.Address{}) { // there was no unused wallet
						// no representative available for this payload's payout recipient
						// as all wallets configured have a payout recipient in the k2 lending contracts that do not match the payload's fee recipient thus
						// these validators cannot be registered under this node operator's representative address as they do not pay to the specified payout recipient

						// however check if the module is configured for web3signer operations and a payout recipient has been set
						if k2.cfg.Web3SignerUrl != nil {
							// if the k2 web3signer has been set
							// then can used the first priority wallet as the representative address as all configured wallets have a payout recipient in the k2 lending contracts

							representative = firstUsedRepresentative
							k2.log.WithField("representative", representative.Address.String()).Debug("using the first priority wallet as the representative address as all configured wallets have a payout recipient in the k2 lending contracts that do not match the payload's fee recipient")

							// since the wallet has already been used we cannot use the global payout recipient to overwrite the payload's fee recipient and would need to use the existing payout recipient for this representative
							payloadFeeRecipient = nodeOperatorTopayoutRecipientMapping[representative.Address.String()]
							k2.log.WithField("payoutRecipient", payloadFeeRecipient.String()).Debug("using the payout recipient set in the k2 contract to overwrite the payload's fee recipient for K2")

						} else {
							// if the module is not configured for web3signer operations then throw an error as nothing can be done as all wallets are used
							k2.log.WithField("payloadFeeRecipient", payloadFeeRecipient.String()).Error("no representative available for this payload's payout recipient. Add another wallet to your config")
							preChecksError.Store(fmt.Errorf("no configured representative available for this payload's fee recipient [%s]; add another wallet to your config", payloadFeeRecipient.String()))
							return
						}
					} else {
						// use the next available representative address that has not been used
						representative = unusedRepresentative

						// if a global payout recipient has been set in the configuration and it doesnt match the
						// payout recipient overwrite the payload's fee recipient and use the set global payout recipient
						// for this unused representative address
						if k2.cfg.Web3SignerUrl != nil && k2.cfg.PayoutRecipient != (common.Address{}) {
							payloadFeeRecipient = k2.cfg.PayoutRecipient
							k2.log.WithField("payoutRecipient", payloadFeeRecipient.String()).Debug("using the payout recipient set in the configuration to overwrite the payload's fee recipient for K2")
						}
					}
				}
			}

			// re-assign the payout recipient if it has been changed
			// from the seletion of the representative address and payout recipient
			setPayoutRecipient = payloadFeeRecipient

			if globalMaxNativeDelegation != nil && currentGlobalNativeDelegation != nil && globalMaxNativeDelegation.Cmp(currentGlobalNativeDelegation) <= 0 {
				// global max native delegation has been reached
				// so need to check individual max native delegation
				k2.log.WithField("globalMaxNativeDelegation", globalMaxNativeDelegation.String()).Debug("global max native delegation has been reached")

				isInInclusionList, err := k2.eth1.K2CheckInclusionList(representative.Address)
				if err != nil {
					k2.log.WithError(err).Error("failed to check if validator's representative address is in inclusion list")
					preChecksError.Store(err)
					return
				}

				if !isInInclusionList {
					// validator representative is not in the inclusion list
					// so cannot natively delegate this validator
					k2.log.WithField("validatorRepresentative", representative.Address.String()).Debug("validator's representative is not in the inclusion list")
					// this is not an error and just means that the validator's representative address is not in the inclusion list to exceed the global max native delegation
					preChecksComplete.Store(true)
					return
				}

				// use a sync wait group to wait for the results of the individual native delegation checks
				var wg sync.WaitGroup
				wg.Add(2)

				go func() {
					defer wg.Done()
					var err error
					individualMaxNativeDelegation, err = k2.eth1.IndividualMaxNativeDelegation()
					if err != nil {
						k2.log.WithError(err).Errorf("failed to get individual max native delegation for representative: %s", representative.Address.String())
						preChecksError.Store(err)
						return
					}
					k2.log.WithField("individualMaxNativeDelegation", individualMaxNativeDelegation.String()).Debug("individual max native delegation")
				}()

				go func() {
					defer wg.Done()
					var err error
					currentIndividualNativeDelegation, err := k2.eth1.K2CheckInclusionListKeysCount(representative.Address)
					if err != nil {
						k2.log.WithError(err).Errorf("failed to get current individual native delegation for representative: %s", representative.Address.String())
						preChecksError.Store(err)
						return
					}
					k2.log.WithField("currentIndividualNativeDelegation", currentIndividualNativeDelegation.String()).Debugf("current individual native delegation for representative: %s", representative.Address.String())
				}()

				wg.Wait()

				if preChecksError.Load() != nil {
					// error occurred while getting the individual native delegation
					// so cannot proceed with the registrations
					k2.log.WithError(preChecksError.Load().(error)).Error("failed to get individual native delegation")
					return
				}

				preChecksComplete.Store(true)
			} else {
				// global max native delegation has not been reached
				// so no need to check individual max native delegation
				k2.log.WithField("globalMaxNativeDelegation", globalMaxNativeDelegation.String()).Debug("global max native delegation has not been reached")
				preChecksComplete.Store(true)
			}
		} else {
			// If the module is configured for Proposer Registry operations only
			// then no need to check the global max native delegation

			// TODO: ROnny

			preChecksComplete.Store(true)
			k2.log.Debug("module is configured for Proposer Registry operations only, K2 capacity and represenative checks not required")
		}

	}()

	// if no recepient is set then use the default primary representative address to to process this registration batch
	// else if there is already a fee recpient set then check if the module has that wallet configured and use that representative address

	validators, payloadMap := k2common.GetListOfBLSKeysFromSignedValidatorRegistration(payload)

	k2.log.WithField("validators", len(validators)).Info("Checking Proposer Registry registrations")
	proposerRegistryResults, err := k2.eth1.BatchCheckRegisteredValidators(validators)
	if err != nil {
		k2.log.WithError(err).Error("failed to check if validators are already proposerRegistry registered")
		return nil, err
	}

	// for registering in the Proposer Registry
	var registrationsToProcess = make(map[string]apiv1.SignedValidatorRegistration)
	var alreadyRegisteredMap = make(map[string]k2common.K2ValidatorRegistration)

	for validator, registered := range proposerRegistryResults {
		if registered.Status == 0 { // not registered
			if excludedValidator, ok := k2.exclusionList[validator]; ok {
				if excludedValidator.ExcludedFromProposerRegistration {
					k2.log.WithField("validatorPubKey", validator).Debug("validator is excluded from Proposer Registry registration")
					continue
				}
			}
			registrationsToProcess[validator] = payloadMap[validator]
		} else {
			alreadyRegisteredMap[validator] = k2common.K2ValidatorRegistration{
				RepresentativeAddress:   registered.Representative,
				ProposerRegistrySuccess: true,
				SignedValidatorRegistration: &apiv1.SignedValidatorRegistration{ // structure modified here from that of the payload
					Message: &apiv1.ValidatorRegistration{
						Pubkey:       payloadMap[validator].Message.Pubkey,
						GasLimit:     payloadMap[validator].Message.GasLimit,
						FeeRecipient: bellatrix.ExecutionAddress(registered.PayoutRecipient),
						Timestamp:    payloadMap[validator].Message.Timestamp,
					},
					Signature: payloadMap[validator].Signature,
				},
			}
		}
	}
	proposerRegistryAlreadyRegisteredCount := uint64(len(alreadyRegisteredMap))

	waitLogged := false
	for !preChecksComplete.Load() && preChecksError.Load() == nil {
		if !waitLogged {
			k2.log.Debug("waiting for pre checks to complete")
			waitLogged = true
		}
	}

	// if there is an error in the prechecks then return the error
	if preChecksError.Load() != nil {
		return nil, preChecksError.Load().(error)
	}

	///////////////////////////////////////////////////////////////
	// PRECHECKS, WALLET AND PAYOUT RECIPIENT SELECTION COMPLETED//
	///////////////////////////////////////////////////////////////

	// prepare registrations from the registrationsToProcess for the proposer registry
	processValidators, err := k2.prepareRegistrations(registrationsToProcess, representative.Address, setPayoutRecipient)
	if err != nil {
		return nil, err
	}

	var k2AlreadyRegisteredCount uint64
	var k2UnsuppportedCount uint64

	if k2.cfg.K2LendingContractAddress != (common.Address{}) {
		// If the module is configured for K2 operations in addition to Proposer Registry operations

		k2.log.WithField("validators", len(validators)).Info("Checking K2 registrations")
		k2RegistrationResults, err := k2.eth1.BatchK2CheckRegisteredValidators(validators)
		if err != nil {
			k2.log.WithError(err).Error("failed to check if validators are already registered")
			return nil, fmt.Errorf("failed to check if validators are already registered: %v", err)
		}

		var preprareK2OnlyRegistrationsToProcess map[string]apiv1.SignedValidatorRegistration = make(map[string]apiv1.SignedValidatorRegistration)
		var signablePubKeys map[string]bool = make(map[string]bool)
		if k2.cfg.Web3SignerUrl != nil {
			signablePubKeys, err = k2.web3Signer.GetPubkeyList()
			if err != nil {
				k2.log.WithError(err).Error("failed to get signable pubkeys")
				return nil, fmt.Errorf("failed to get signable pubkeys: %v", err)
			}
		}

		// prepare K2 only registrations
		for validator, registered := range k2RegistrationResults {
			if strings.EqualFold(registered, common.Address{}.String()) {
				// this is a validator that is not registered in the K2 contract
				if registration, ok := alreadyRegisteredMap[validator]; !ok {
					// this is a validator that is not registered in the Proposer Registry
					// check to see if this is being handled by the registrationToProcess
					if _, ok := registrationsToProcess[validator]; !ok {
						// this should never happen unless the validator key has been excluded from the Proposer Registry registration
						if excludedValidator, ok := k2.exclusionList[validator]; ok {
							if !excludedValidator.ExcludedFromProposerRegistration {
								k2.log.WithField("validatorPubKey", validator).Errorf("validator is not registered in the Proposer Registry and is not being handled by the registrationToProcess")
							}
						} else {
							if (!strings.EqualFold(setPayoutRecipient.String(), payload[0].Message.FeeRecipient.String()) && setPayoutRecipient != common.Address{} && k2.cfg.Web3SignerUrl != nil) {
								// if the payout recipient has been set and is different from the one in the payload
								// means there was a change in the payout recipient and required the payload to be signed again
								// so the validator was not in configurable with the web3signer for the new registeration message
								// to be signed, thus skip this validator as unsupported
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey":           validator,
									"configuredPayoutRecipient": setPayoutRecipient.String(),
									"payloadPayoutRecipient":    payload[0].Message.FeeRecipient.String(),
								}).Debug("validator is not registered in the Proposer Registry and the payout recipient has changed between the registration message and the selected payout address")
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey": validator,
								}).Debug("could not sign the registration message for the validator for the new payout recipient")
							} else {
								k2.log.WithField("validatorPubKey", validator).Errorf("validator is not registered in the Proposer Registry and is not being handled by the registrationToProcess")
							}
						}

						continue // continue to the next validator as even the proposer registry is not processing this validator thus it is unsupported
					} else {
						// this is a validator that is not registered in the K2 contract
						// but is being handled by the registrationToProcess
						continue // so continue to the next validator as it is being handled by the Proposer Registry and would also then be natively delegated
					}

				} else {
					// this is a validator that is already registered in the Proposer Registry, but not in the K2 contract
					// so it would NOT be in the registrations mapping from the proposerRegistry registrations to process
					// perform further checks on this validator to add it to the registration mapping if it must be natively delegated

					// check if the validator is excluded from native delegation, before adding it as a registration to process
					if excludedValidator, ok := k2.exclusionList[validator]; ok {
						if excludedValidator.ExcludedFromNativeDelegation {
							k2.log.WithField("validatorPubKey", validator).Debug("validator is excluded from native delegation")
							continue
						}
					}

					// check if the representative address from the proposrRegistry for this validator is the same as the one selected
					if registration.RepresentativeAddress != representative.Address {
						// representative address from proposerRegistry for validator is not the
						// same as the one being used to process the registration
						// so cannot take action on this validator
						k2.log.WithFields(
							logrus.Fields{
								"validatorPubKey":          validator,
								"representativeAddress":    registration.RepresentativeAddress.String(),
								"configuredRepresentative": representative.Address.String(),
							}).Debugf("validator is already registered in the Proposer Registry, but the representative address is not the same as the one configured for this registration")
						k2UnsuppportedCount++
						continue
					}

					// representative address is the same as the one configured so can take action on this validator

					// They are in the already registered map which has the proposer registry success set to true,
					// has the signed validator registration payload with the payout recipient from the proposer registry contract,
					// proposer registry contract, and the representative address already set.
					// the only field not set is the ecdsa sig for this already registered validator that would be needed
					// for further k2 native delegation

					// Check if there is capacity for this validator to be natively delegated
					// if the global max native delegation has been reached then check the individual max native delegation
					if globalMaxNativeDelegation != nil && currentGlobalNativeDelegation != nil && globalMaxNativeDelegation.Cmp(currentGlobalNativeDelegation) <= 0 {
						// global max native delegation has been reached
						// so need to check individual max native delegation
						if isInInclusionList {
							if individualMaxNativeDelegation != nil && currentIndividualNativeDelegation != nil && individualMaxNativeDelegation.Cmp(currentIndividualNativeDelegation) <= 0 {
								// individual max native delegation has been reached
								// so cannot natively delegate this validator
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey":   validator,
									"individualMax":     individualMaxNativeDelegation.String(),
									"currentIndividual": currentIndividualNativeDelegation.String(),
								}).Debugf("validator is already registered in the Proposer Registry, but the global and individual max native delegation has been reached")
								k2UnsuppportedCount++
								continue
							}
						} else {
							// validator representative address is not in the inclusion list
							// so cannot natively delegate this validator since the max has been reached
							k2.log.WithField("validatorPubKey", validator).Debug("validator representative address is not in the inclusion list")
							k2UnsuppportedCount++
							continue
						}
					}

					// Once here, means we can natively delegate this validator from the already registered map for Proposer Registry

					// since the already registered map of proposer registry is modified (from the contract and not the actual current payload), thus the
					// signedValidatorRegistration object is modified, try and find a current valid payload for this validator
					// and add it to the registrations to process for K2 only native delegation
					if validPayload, ok := payloadMap[validator]; ok {

						modifiedPayload := apiv1.SignedValidatorRegistration{
							Message: &apiv1.ValidatorRegistration{
								Pubkey:   validPayload.Message.Pubkey,
								GasLimit: validPayload.Message.GasLimit,
								// if the setPayoutRecipient has been changed from the payload then use that as it means
								// the payload can be signed for the new payout recipient, however if it is the same,
								// no re signing is required as the payload is already signed for the selected payout recipient
								FeeRecipient: bellatrix.ExecutionAddress(setPayoutRecipient), // only field potentially changed from the payload
								Timestamp:    validPayload.Message.Timestamp,
							},
							Signature: validPayload.Signature,
						}

						// check if the setPayoutRecipient has been changed from the payload then check if there is a web3signer to facilitate the change
						if !strings.EqualFold(setPayoutRecipient.String(), validPayload.Message.FeeRecipient.String()) {
							if k2.cfg.Web3SignerUrl == nil {
								// if the payout recipient has been set and is different from the one in the payload
								// means there was a change in the payout recipient and required the payload to be signed again
								// so the validator was not in configurable with the web3signer for the new registeration message
								// to be signed, thus skip this validator as unsupported
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey":         validator,
									"selectedPayoutRecipient": setPayoutRecipient.String(),
									"payloadPayoutRecipient":  validPayload.Message.FeeRecipient.String(),
								}).Debug("validator is already registered in the Proposer Registry and the payout recipient has changed between the registration message and the selected payout address")
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey": validator,
								}).Debug("could not sign the registration message for the validator for the new payout recipient")
								k2UnsuppportedCount++
								continue
							}

							// then we need to re-sign the payload for the new payout recipient
							// as this was not done by the preprareRegistrations function, since we did not preprare
							// registrations for alreadyRegisteredMap as they are already registered in the Proposer Registry
							if _, ok := signablePubKeys[validator]; !ok {
								// if the validator is not in the signable pubkeys list then it cannot be signed
								// for the new payout recipient
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey":         validator,
									"selectedPayoutRecipient": setPayoutRecipient.String(),
									"payloadPayoutRecipient":  validPayload.Message.FeeRecipient.String(),
								}).Debug("validator is already registered in the Proposer Registry and the payout recipient has changed between the registration message and the selected payout address")
								k2.log.WithFields(logrus.Fields{
									"validatorPubKey": validator,
								}).Debug("could not sign the registration message for the validator for the new payout recipient")
								k2UnsuppportedCount++
								continue
							} else {
								// add here to [re-sign AND then generate ECDSA signature] for the new payload to process for k2 only native delegation
								preprareK2OnlyRegistrationsToProcess[validator] = modifiedPayload

								// increase the current global and individual native delegation
								currentGlobalNativeDelegation.Add(currentGlobalNativeDelegation, big.NewInt(1))
								currentIndividualNativeDelegation.Add(currentIndividualNativeDelegation, big.NewInt(1))
							}
						} else {
							// add here to [generate the ECDSA signature] for the new payload to process for k2 only native delegation
							preprareK2OnlyRegistrationsToProcess[validator] = modifiedPayload

							// increase the current global and individual native delegation
							currentGlobalNativeDelegation.Add(currentGlobalNativeDelegation, big.NewInt(1))
							currentIndividualNativeDelegation.Add(currentIndividualNativeDelegation, big.NewInt(1))
						}

					} else {
						k2.log.WithField("validatorPubKey", validator).Error("validator is already registered in the Proposer Registry, but the payload for this validator is not found")
						k2UnsuppportedCount++
					}
				}

			} else {
				// this is a validator that is already registered in the K2 contract meaning it is already registered in the Proposer Registry as well
				// just add to the already registered map with the K2Success flag set to true so that this can be skipped since exists
				// this is just necessary to log and return the results
				if knownRegistration, ok := alreadyRegisteredMap[validator]; !ok {
					// if the validator is not in the already registered map then add it
					// this should not happen as the validator should be in the already registered map
					alreadyRegisteredMap[validator] = k2common.K2ValidatorRegistration{
						RepresentativeAddress:   common.HexToAddress(registered),
						ProposerRegistrySuccess: true,
						K2Success:               true,
						SignedValidatorRegistration: &apiv1.SignedValidatorRegistration{
							Message: &apiv1.ValidatorRegistration{
								Pubkey:       payloadMap[validator].Message.Pubkey,
								GasLimit:     payloadMap[validator].Message.GasLimit,         // ** this may have changed from the gas limit signed as of the previous time of registration
								FeeRecipient: bellatrix.ExecutionAddress(setPayoutRecipient), // may be different from the payload and from what is actually registered in the contracts
								// but safe to assume since this is valid for the current representative address and has already been registered then the deduced payout recipient is valid
								// as it would be a payout recipient of the representative address from the contracts of the already used representative address
								Timestamp: payloadMap[validator].Message.Timestamp, // ** this is the timestamp of the payload, but not the time stamp of the actual payload that was signed as of previous time of registration
							},
							Signature: payloadMap[validator].Signature,
						},
					} // the above payload is not the actual payload that was signed as of the previous time of registration this is just to log and return the results
					// important fields are the ProposerRegistrySuccess, K2Success, RepresentativeAddress, and PubKey fields
					// fields marked ** are not obtainable from the contracts as such would use the current payload fields in returning the result for logging/output
				} else {
					// if the validator is in the already registered map then update the K2Success flag
					r := knownRegistration
					r.K2Success = true
					alreadyRegisteredMap[validator] = r
				}

				k2AlreadyRegisteredCount++
			}
		}

		if len(preprareK2OnlyRegistrationsToProcess) > 0 {
			// Generate the ECDSA Signatures for the validators who are to perform K2 Only native delegation as these would not have been generated by
			// the previous Proposer Only Registration Preparations. If the validator is not in the alreadyREgistered mapping and is in the registrations to
			// process then it would already have an ECDSA Signature from the signature swapper that it woul use for both registration and native delegation

			// Generate the signatures for the K2-only native delegations that were just added to the mapping
			processK2Validators, err := k2.prepareRegistrations(preprareK2OnlyRegistrationsToProcess, representative.Address, setPayoutRecipient)
			if err != nil {
				return nil, err
			}

			for validatorPubKey, reg := range processK2Validators {
				// Once the registrations have been re-signed for amodified payout if thats the case
				// and also ecdsa signatures have been generated for the new payload for k2 only native delegation
				// then add these to the processValidators mapping for final processing
				r := reg
				r.ProposerRegistrySuccess = true
				processValidators[validatorPubKey] = r // safe to do this as it wont overwrite any existing in the map
				// since the validator if was in the registrations to process would not havve been in the alreadyRegisteredMap and thus not have been in the
				// prepareK2OnlyRegistrationsToProcess mapping
			}
		}

	}

	///////////////////////////
	// PREPARATION COMPLETE //
	///////////////////////////

	// Final processing of the registrations to the contract executions
	var proposerRegistrations []k2common.K2ValidatorRegistration
	var k2Registrations []k2common.K2ValidatorRegistration

	for _, processingDetails := range processValidators {
		if processingDetails.ProposerRegistrySuccess {
			// already registered in the Proposer Registry
			// send the registration to the K2 contract only
			// no need to check if excluded as it wont be in the mapping here if excluded from k2-only native delegation
			if k2.cfg.K2LendingContractAddress != (common.Address{}) {
				k2Registrations = append(k2Registrations, processingDetails)
			}
		} else {
			// not registered in the Proposer Registry
			// send the registration to the Proposer Registry
			// and send the registration to the K2 contract
			// need to check if excluded is it may be in the mapping for only proposer registry registration
			proposerRegistrations = append(proposerRegistrations, processingDetails)
			if k2.cfg.K2LendingContractAddress != (common.Address{}) {
				if excludedValidator, ok := k2.exclusionList[processingDetails.SignedValidatorRegistration.Message.Pubkey.String()]; ok {
					if !excludedValidator.ExcludedFromNativeDelegation {
						k2Registrations = append(k2Registrations, processingDetails)
					}
				} else {
					k2Registrations = append(k2Registrations, processingDetails)
				}
			}
		}
	}

	if len(proposerRegistrations) > 0 {
		k2.log.WithFields(logrus.Fields{
			"proposerRegistrations": len(proposerRegistrations),
			"alreadyRegistered":     proposerRegistryAlreadyRegisteredCount,
		}).Infof("Registering %v validators in the Proposer Registry.", len(proposerRegistrations))
		tx, err := k2.eth1.BatchRegisterValidators(proposerRegistrations)
		if err != nil {
			k2.log.WithError(err).Error("failed to register validators in the Proposer Registry")
			return nil, err
		}
		k2.log.WithFields(logrus.Fields{
			"newRegistrations": len(proposerRegistrations),
			"txHash":           tx.Hash().String(),
		}).Info("Proposer Registry registration transaction completed")
		// update the proposerRegistrySuccess status here as no error was returned from execution
		for _, registration := range proposerRegistrations {
			r := processValidators[registration.SignedValidatorRegistration.Message.Pubkey.String()]
			r.ProposerRegistrySuccess = true
			processValidators[registration.SignedValidatorRegistration.Message.Pubkey.String()] = r
		}
	} else {
		k2.log.WithField("alreadyRegistered", proposerRegistryAlreadyRegisteredCount).Info("No new validators to register in the Proposer Registry")
	}

	if len(k2Registrations) > 0 && k2.cfg.K2LendingContractAddress != (common.Address{}) {
		k2.log.WithFields(logrus.Fields{
			"k2Registrations":   len(k2Registrations),
			"alreadyRegistered": k2AlreadyRegisteredCount,
			"unsupported":       k2UnsuppportedCount,
		}).Infof("Registering %v validators in the K2 contract.", len(k2Registrations))
		tx, err := k2.eth1.K2BatchNativeDelegation(k2Registrations)
		if err != nil {
			k2.log.WithError(err).Error("failed to register validators in the K2 contract")
			return nil, err
		}
		k2.log.WithFields(logrus.Fields{
			"newRegistrations": len(k2Registrations),
			"txHash":           tx.Hash().String(),
		}).Info("K2 registration transaction completed")
		// update the k2Register status here as no error was returned from execution
		for _, registration := range k2Registrations {
			r := processValidators[registration.SignedValidatorRegistration.Message.Pubkey.String()]
			r.K2Success = true
			processValidators[registration.SignedValidatorRegistration.Message.Pubkey.String()] = r
		}
	} else if k2.cfg.K2LendingContractAddress != (common.Address{}) {
		k2.log.WithFields(
			logrus.Fields{
				"alreadyRegistered": k2AlreadyRegisteredCount,
				"unsupported":       k2UnsuppportedCount,
			},
		).Info("No new supported validators to register in the K2 contract")
	}

	var results []k2common.K2ValidatorRegistration
	for _, registration := range processValidators {
		results = append(results, registration)
	}

	if len(processValidators) > 0 {
		k2.log.WithFields(
			logrus.Fields{
				"newRegistrations":                   len(processValidators),
				"newRegistrationsInProposerRegistry": len(proposerRegistrations),
				"newRegistrationsInK2":               len(k2Registrations),
			},
		).Info("Validator registrations successfully processed")
	}

	return results, nil
}

func (k2 *K2Service) processClaim(blsKeys []phase0.BLSPubKey) ([]k2common.K2Claim, error) {
	k2.lock.Lock()
	defer k2.lock.Unlock()

	if k2.cfg.K2LendingContractAddress == (common.Address{}) || k2.cfg.K2NodeOperatorContractAddress == (common.Address{}) {
		// module not configured to run
		return nil, fmt.Errorf("module not configured to run K2 contract operations")
	} else if k2.cfg.BalanceVerificationUrl == nil {
		// module not configured to run
		return nil, fmt.Errorf("module not configured to run balance verification operations for claims")
	}

	k2.log.WithField("validators", len(blsKeys)).Info("Checking K2 claims")
	claimable, err := k2.eth1.BatchK2CheckClaimableRewards(blsKeys)
	if err != nil {
		k2.log.WithError(err).Error("failed to check if validators have claimable rewards")
		return nil, err
	}

	var claimmableValidators []phase0.BLSPubKey
	var claimMap = make(map[string]k2common.K2Claim)
	var claimsToProcess []k2common.K2Claim

	var totalClaimed *big.Float = big.NewFloat(0)

	for _, validator := range blsKeys {
		if claimableAmount, ok := claimable[validator.String()]; ok {
			if claimableAmount > uint64(k2.cfg.ClaimThreshold*math.Pow(10, float64(k2common.KETHDecimals))) {
				claim := k2common.K2Claim{
					ValidatorPubKey: validator,
					ClaimAmount:     claimableAmount,
				}
				claimMap[validator.String()] = claim
				claimmableValidators = append(claimmableValidators, validator)
			} else {
				k2.log.WithFields(logrus.Fields{
					"validator":        validator.String(),
					"claimableRewards": claimableAmount,
				}).Debug("Validator rewards are insufficient to claim")
			}
		}
	}

	if len(claimmableValidators) > 0 {

		// if there are claims then get the effective balance of these validators and
		// report to the balance verifier for signatures for the claims
		effectiveBalances, err := k2.beacon.FinalizedValidatorEffectiveBalance(claimmableValidators)
		if err != nil {
			k2.log.WithError(err).Error("failed to get effective balances for validators")
			return nil, err
		}

		var qualifiedValidators map[phase0.BLSPubKey]uint64 = make(map[phase0.BLSPubKey]uint64)
		for validator, effectiveBalance := range effectiveBalances {
			claim := claimMap[validator.String()]
			claim.EffectiveBalance = effectiveBalance
			claimMap[validator.String()] = claim
			// validators with effective balance less than 32 ETH in a claim attempt
			// would be kicked from the protocol. Best to avoid the software automatically
			// causing such to the user without the user's own direct action
			if effectiveBalance == 32000000000 {
				qualifiedValidators[validator] = effectiveBalance
			} else {
				k2.log.WithFields(logrus.Fields{
					"validator":        validator.String(),
					"effectiveBalance": effectiveBalance,
					"claimableRewards": claimable[validator.String()],
				}).Debugf("Validator has effective balance less than 32 ETH, not processing claim")
			}
		}

		if len(qualifiedValidators) == 0 {
			k2.log.WithField("validators", len(claimmableValidators)).Info("No validators with effective balance of 32 ETH")
			return nil, nil
		}

		verifiedEffectiveBalances, err := k2.balanceverifier.ReportEffectiveBalance(qualifiedValidators)
		if err != nil {
			k2.log.WithError(err).Error("failed to get verified effective balances for validators")
			return nil, err
		}

		for v := range qualifiedValidators {
			if effectiveBalanceSig, ok := verifiedEffectiveBalances[v]; ok {
				updatedClaim := claimMap[v.String()]
				updatedClaim.ECDSASignature = effectiveBalanceSig
				claimsToProcess = append(claimsToProcess, updatedClaim)
				claimMap[v.String()] = updatedClaim
				amountDecimal := big.NewFloat(0).Quo(big.NewFloat(float64(updatedClaim.ClaimAmount)), big.NewFloat(math.Pow(10, float64(k2common.KETHDecimals))))
				totalClaimed.Add(totalClaimed, amountDecimal)
			}
		}

		k2.log.WithFields(logrus.Fields{
			"claims": len(claimsToProcess),
			"amount": totalClaimed.String() + " KETH",
		}).Infof("Processing %v claims through K2 module", len(claimMap))
		tx, err := k2.eth1.BatchK2ClaimRewards(claimsToProcess)
		if err != nil {
			k2.log.WithError(err).Error("failed to claim rewards from the K2 contract")
			return nil, err
		}
		k2.log.WithFields(logrus.Fields{
			"claims": len(claimsToProcess),
			"amount": totalClaimed.String() + " KETH",
			"txHash": tx.Hash().String(),
		}).Info("K2 claim transaction completed")
		// update the claim status here as no error was returned from execution
		for _, claim := range claimsToProcess {
			c := claimMap[claim.ValidatorPubKey.String()]
			c.ClaimSuccess = true
			claimMap[claim.ValidatorPubKey.String()] = c
		}
	} else {
		k2.log.WithField("claims", len(claimMap)).Info("No new claims to process")
		return nil, nil
	}

	var results []k2common.K2Claim
	for _, claim := range claimMap {
		results = append(results, claim)
	}

	k2.log.WithFields(logrus.Fields{
		"claimsRequested:":          len(blsKeys),
		"qualifiedClaimsProcessed:": len(claimsToProcess),
		"amount":                    totalClaimed.String() + " KETH",
	}).Info("K2 claims successfully processed")

	return results, nil
}

func (k2 *K2Service) processExit(blsKey phase0.BLSPubKey) (res k2common.K2Exit, err error) {
	k2.lock.Lock()
	defer k2.lock.Unlock()

	if k2.cfg.K2LendingContractAddress == (common.Address{}) || k2.cfg.K2NodeOperatorContractAddress == (common.Address{}) {
		// module not configured to run
		return res, fmt.Errorf("module not configured to run K2 contract operations")
	} else if k2.cfg.BalanceVerificationUrl == nil {
		// module not configured to run
		return res, fmt.Errorf("module not configured to run balance verification operations for exits")
	}

	k2.log.Info("Checking Validator Representative Address")

	k2RegistrationResults, err := k2.eth1.BatchK2CheckRegisteredValidators([]phase0.BLSPubKey{blsKey})
	if err != nil {
		k2.log.WithError(err).Error("failed to check if validator is already registered")
		return res, fmt.Errorf("failed to check if validator is already registered")
	}

	representativeAddress, ok := k2RegistrationResults[blsKey.String()]
	if !ok {
		k2.log.WithField("validator", blsKey.String()).Error("validator is not registered in the K2 contract")
		return res, fmt.Errorf("validator is not registered in the K2 contract")
	}

	var representative k2common.ValidatorWallet = k2.cfg.ValidatorWallets[0]
	for _, wallet := range k2.cfg.ValidatorWallets {
		if strings.EqualFold(wallet.Address.String(), representativeAddress) {
			representative = wallet
			break
		}
	}

	if !strings.EqualFold(representativeAddress, representative.Address.String()) {
		k2.log.WithFields(logrus.Fields{
			"configuredRepresentative": representative.Address.String(),
			"validatorRepresentative":  representativeAddress,
		}).Error("Validator Representative Address does not match configured representative address")
		return res, fmt.Errorf("validator representative address does not match any configured representative address")
	}

	res.ValidatorPubKey = blsKey
	res.RepresentativeAddress = representative.Address

	// if authorized then get the effective balance of this validator and
	// report to the balance verifier for a signature for the exit (un-delegation)
	effectiveBalances, err := k2.beacon.FinalizedValidatorEffectiveBalance([]phase0.BLSPubKey{blsKey})
	if err != nil {
		k2.log.WithError(err).Error("failed to get effective balance for validator")
		return res, fmt.Errorf("failed to get effective balance for validator")
	}

	res.EffectiveBalance = effectiveBalances[blsKey]

	var report = make(map[phase0.BLSPubKey]uint64)
	report[blsKey] = effectiveBalances[blsKey]

	verifiedEffectiveBalances, err := k2.balanceverifier.ReportEffectiveBalance(report)
	if err != nil {
		k2.log.WithError(err).Error("failed to get verified effective balance for validator")
		return res, fmt.Errorf("failed to get verified effective balance for validator")
	}

	res.ECDSASignature = verifiedEffectiveBalances[blsKey]

	k2.log.WithFields(logrus.Fields{
		"validator": blsKey.String(),
	}).Info("Exiting validator from K2 contract")

	tx, err := k2.eth1.K2Exit(res)
	if err != nil {
		k2.log.WithError(err).Error("failed to exit the validator from the K2 contract")
		return res, fmt.Errorf("failed to exit the validator from the K2 contract: %w", err)
	}
	k2.log.WithFields(logrus.Fields{
		"validator": blsKey.String(),
		"txHash":    tx.Hash().String(),
	}).Info("K2 validator exit transaction completed")
	// update the exit status here as no error was returned from execution
	res.ExitSuccess = true

	k2.log.WithFields(logrus.Fields{
		"validator": blsKey.String(),
	}).Info("K2 validator exit successfully processed")

	return res, nil
}

func (k2 *K2Service) batchProcessClaims(blsKeys []phase0.BLSPubKey) ([]k2common.K2Claim, error) {

	if k2.cfg.K2LendingContractAddress == (common.Address{}) {
		// module not configured to run
		return nil, fmt.Errorf("module not configured to run K2 contract operations")
	} else if k2.cfg.BalanceVerificationUrl == nil {
		// module not configured to run
		return nil, fmt.Errorf("module not configured to run balance verification operations for claims")
	}

	// Split the payload into batches of 90 for the sake of gas efficiency
	var batches [][]phase0.BLSPubKey
	for i := 0; i < len(blsKeys); i += 90 {
		end := i + 90
		if end > len(blsKeys) {
			end = len(blsKeys)
		}
		batches = append(batches, blsKeys[i:end])
	}

	var results []k2common.K2Claim
	for i, batch := range batches {
		k2.log.WithFields(logrus.Fields{
			"currentBatchClaims": len(batch),
			"totalClaims":        len(blsKeys),
		}).Infof("Processing %v claims through K2 module [batch %v/%v]", len(batch), i+1, len(batches))
		batchResults, err := k2.processClaim(batch)
		if err != nil {
			return nil, err
		}
		results = append(results, batchResults...)
	}

	return results, nil
}

func (k2 *K2Service) batchProcessValidatorRegistrations(payload []apiv1.SignedValidatorRegistration) ([]k2common.K2ValidatorRegistration, error) {

	if len(payload) == 0 {
		return nil, nil
	}

	var feeRecipientMapping = make(map[string][]apiv1.SignedValidatorRegistration)
	for _, reg := range payload {
		feeRecipientMapping[reg.Message.FeeRecipient.String()] = append(feeRecipientMapping[reg.Message.FeeRecipient.String()], reg)
	}
	// then group in new sortedPayload by feeRecipient
	var sortedPayload []apiv1.SignedValidatorRegistration
	for feeRecipient, regs := range feeRecipientMapping {
		sortedPayload = append(sortedPayload, regs...)
		k2.log.Debugf("Payload contains registration for fee recipient: %s", feeRecipient)
	}
	payload = sortedPayload

	// Group the payload into batches of 90 for gas efficiency, contract calls, and signature swapping,
	// ensuring batches have the same fee recipient
	var batches [][]apiv1.SignedValidatorRegistration

	var currentRecipient string
	var currentBatch []apiv1.SignedValidatorRegistration
	for i, registration := range payload {
		if i > 0 && (len(currentBatch) == 90 || !strings.EqualFold(registration.Message.FeeRecipient.String(), currentRecipient)) {
			// Append the current batch to batches if it's full or the fee recipient changes
			batches = append(batches, currentBatch)
			currentBatch = nil
		}
		// Update the current fee recipient if it's not set
		if currentRecipient == "" {
			currentRecipient = registration.Message.FeeRecipient.String()
		}
		// Append the current registration to the current batch
		currentBatch = append(currentBatch, registration)
	}
	// Append the remaining batch
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	var results []k2common.K2ValidatorRegistration
	for i, batch := range batches {
		k2.log.WithFields(logrus.Fields{
			"currentBatchRegistrations": len(batch),
			"totalRegistrations":        len(payload),
		}).Infof("Processing %v validator registrations through K2 module [batch %v/%v]", len(batch), i+1, len(batches))
		batchResults, err := k2.processValidatorRegistrations(batch)
		if err != nil {
			return nil, err
		}
		results = append(results, batchResults...)
	}

	return results, nil
}

func (k2 *K2Service) prepareRegistrations(toProcess map[string]apiv1.SignedValidatorRegistration, representative common.Address, payoutRecipient common.Address) (map[string]k2common.K2ValidatorRegistration, error) {

	var registrations map[string]k2common.K2ValidatorRegistration = make(map[string]k2common.K2ValidatorRegistration)
	if len(toProcess) == 0 {
		return registrations, nil
	}

	// Retrieve the signable pubkeys from the web3signer on runtime since these can change whenever the node runner wishes
	// only if web3signer is configured

	var signablePubKeys map[string]bool = make(map[string]bool)
	var err error
	if k2.cfg.Web3SignerUrl != nil {
		signablePubKeys, err = k2.web3Signer.GetPubkeyList()
		if err != nil {
			k2.log.WithError(err).Error("failed to get signable pubkeys")
			return nil, err
		}
	}
	if payoutRecipient != (common.Address{}) {
		// extract the payout recipient from the payload and check if it is different from the selected payout recipient
		var toProcessFeeRecipient common.Address
		for _, registration := range toProcess {
			toProcessFeeRecipient = common.HexToAddress(registration.Message.FeeRecipient.String())
			break
		}
		if k2.cfg.Web3SignerUrl == nil && !strings.EqualFold(payoutRecipient.String(), toProcessFeeRecipient.String()) {
			// this idealy should not happen as it only would be different from the payload if there is a web3 signer configured; checked through the previous steps
			k2.log.WithField("payoutRecipient", payoutRecipient.String()).Error("Payout recipient is set but web3 signer is not configured to sign with a different payout recipient")
			return nil, fmt.Errorf("payout recipient is set but web3 signer is not configured to sign with a different payout recipient")
		}
	}

	var signedRegistrations []apiv1.SignedValidatorRegistration

	// prepare Proposer Registry registrations
	for validator, registration := range toProcess {
		var signedRegistration apiv1.SignedValidatorRegistration = registration

		if payoutRecipient != (common.Address{}) && !strings.EqualFold(payoutRecipient.String(), registration.Message.FeeRecipient.String()) {
			// if a custom payout recipient selected, different from the payload
			// check if the validator is in the signable keys list
			// to allow for the signing of a new registration with changed payout recipient
			if _, ok := signablePubKeys[validator]; !ok {
				// validator is not in the signable list so cannot generate a new signature
				// then maintain the original payout recipient and make this registration unsupportable
				k2.log.WithFields(logrus.Fields{
					"validatorPubKey":         validator,
					"payloadPayoutRecipient":  registration.Message.FeeRecipient.String(),
					"selectedPayoutRecipient": payoutRecipient.String(),
				}).Error("validator is not in the signable list so cannot generate a new signature, skipping validator")
				continue
			} else {
				// validator is in the signable list so can generate a new signature
				// then use the custom payout recipient
				k2.log.WithFields(logrus.Fields{
					"validatorPubKey":         validator,
					"payloadPayoutRecipient":  registration.Message.FeeRecipient.String(),
					"selectedPayoutRecipient": payoutRecipient.String(),
				}).Debug("validator is in the signable list so can generate a new signature")

				if k2.cfg.Web3SignerUrl != nil {
					// if a custom the payout recipient is configured, different from the payload
					// and a web3 signer is configured, sign the registration with the custom payout recipient

					// this condition would only be hit if the signable key check passed and the payout recipint has been
					// set different from the payload through the preceeding steps
					signedRegistration, err = k2.web3Signer.SignRegistration(
						bellatrix.ExecutionAddress(payoutRecipient),
						registration.Message.GasLimit,
						registration.Message.Pubkey,
						registration.Message.Timestamp,
					)
					if err != nil {
						k2.log.WithError(err).Error("failed to sign registration with custom payout recipient")
						return nil, fmt.Errorf("failed to sign registration with custom payout recipient: %w", err)
					}
				}
			}
		} // else the payout recipient is the same as the payload so signed registration has already been set

		// Create mapping of qualified registrations to proceed
		registrations[signedRegistration.Message.Pubkey.String()] = k2common.K2ValidatorRegistration{
			RepresentativeAddress:       representative,
			SignedValidatorRegistration: &signedRegistration,
		}
		signedRegistrations = append(signedRegistrations, signedRegistration)

	}

	// Generate signatures for the qualified registration messages
	ecdsaSignatures, err := k2.signatureSwapper.BatchGenerateSignature(signedRegistrations, representative)
	if err != nil {
		k2.log.WithError(err).Error("failed to generate signatures from signature swapper for proposer registration")
		return nil, err
	}

	for validatorPubKey, reg := range registrations {
		reg.ECDSASignature = ecdsaSignatures[reg.SignedValidatorRegistration.Message.Pubkey]
		registrations[validatorPubKey] = reg
	}

	return registrations, nil
}
