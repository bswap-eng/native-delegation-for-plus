package signatureswapper

import (
	apiv1 "github.com/attestantio/go-builder-client/api/v1"
	phase0 "github.com/attestantio/go-eth2-client/spec/phase0"
	k2Common "github.com/bswap-eng/native-delegation-for-plus/common"
	"github.com/ethereum/go-ethereum/common"
)

type SignatureSwapResponse struct {
	OriginalData   *apiv1.SignedValidatorRegistration `json:"originalData"`
	EcdsaSignature k2Common.EcdsaSignature            `json:"ecdsaSignature"`
}

type BatchSignatureSwapResponse struct {
	OriginalData    []OriginalDataForBatchResponse `json:"originalData"`
	EcdsaSignatures []k2Common.EcdsaSignature      `json:"ecdsaSignatures"`
}

type OriginalDataForBatchResponse struct {
	Message               *apiv1.ValidatorRegistration `json:"message"`
	RepresentativeAddress common.Address               `json:"representativeAddress"`
	Signature             phase0.BLSSignature          `json:"signature"`
}

type Info struct {
	ChainID                        uint64 `json:"CHAIN_ID,string"`
	BlsDomain                      string `json:"BLS_DOMAIN"`
	GasLimitProposerRegistryDomain uint64 `json:"GAS_LIMIT_PROPOSER_REGISTRY_DOMAIN,string"`
}

type BatchSignatureSwapPayload struct {
	Signatures []SignatureSwapPayload `json:"signatures"`
}

type SignatureSwapPayload struct {
	Signature             phase0.BLSSignature          `json:"signature"`
	Message               *apiv1.ValidatorRegistration `json:"message"`
	RepresentativeAddress common.Address               `json:"representativeAddress"`
}
