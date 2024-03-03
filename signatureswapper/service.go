package signatureswapper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"time"

	apiv1 "github.com/attestantio/go-builder-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethereum/go-ethereum/common"
	k2Common "github.com/restaking-cloud/native-delegation-for-plus/common"

	"github.com/restaking-cloud/native-delegation-for-plus/signatureswapper/config"
)

type SignatureSwapperService struct {
	client *http.Client
	cfg    config.SignatureSwapperConfig
}

func NewSignatureSwapperService() *SignatureSwapperService {
	return &SignatureSwapperService{
		client: &http.Client{Timeout: 12 * time.Second}, // get a response within a slot
	}
}

func (s *SignatureSwapperService) Configure(url *url.URL) error {

	if url == nil {
		return fmt.Errorf("signatureswapperservice: url not set, cannot configure service")
	}

	s.cfg.Url = url

	info, err := s.GetInfo()
	if err != nil {
		return fmt.Errorf("signatureswapperservice: failed to get info: %w", err)
	}

	s.cfg.Domain = info.GasLimitProposerRegistryDomain
	s.cfg.ChainID = big.NewInt(int64(info.ChainID))

	return nil
}

func (s *SignatureSwapperService) Domain() uint64 {
	return s.cfg.Domain
}

func (s *SignatureSwapperService) ConnectedChainId() *big.Int {
	return s.cfg.ChainID
}

func (s *SignatureSwapperService) GetInfo() (Info, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", s.cfg.Url.String()+InfoPath, nil)
	if err != nil {
		return Info{}, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return Info{}, err
	}

	if resp.StatusCode != 200 {
		return Info{}, fmt.Errorf("invalid response (%d): %v", resp.StatusCode, resp)
	}

	var response Info
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return Info{}, err
	}

	return response, nil
}

func (s *SignatureSwapperService) GenerateSignature(
	registration apiv1.SignedValidatorRegistration,
	representativeAddress common.Address,
) (k2Common.EcdsaSignature, error) {

	payload := SignatureSwapPayload{
		Signature:             registration.Signature,
		Message:               registration.Message,
		RepresentativeAddress: representativeAddress,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return k2Common.EcdsaSignature{}, err
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", s.cfg.Url.String()+GenerateSignaturePath, bytes.NewReader(payloadBytes))
	if err != nil {
		return k2Common.EcdsaSignature{}, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return k2Common.EcdsaSignature{}, err
	}

	if resp.StatusCode != 200 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return k2Common.EcdsaSignature{}, fmt.Errorf("error reading invalid response (%d) body: %v", resp.StatusCode, err)
		}

		return k2Common.EcdsaSignature{}, fmt.Errorf("invalid response (%d): %v", resp.StatusCode, string(body))
	}

	var response SignatureSwapResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return k2Common.EcdsaSignature{}, err
	}

	return response.EcdsaSignature, nil
}

func (s *SignatureSwapperService) BatchGenerateSignature(
	registration []apiv1.SignedValidatorRegistration,
	representativeAddress common.Address,
) (map[phase0.BLSPubKey]k2Common.EcdsaSignature, error) {

	payload := BatchSignatureSwapPayload{}

	res := make(map[phase0.BLSPubKey]k2Common.EcdsaSignature)

	if len(registration) == 0 {
		return res, nil
	}

	for _, reg := range registration {
		payload.Signatures = append(payload.Signatures, SignatureSwapPayload{
			Signature:             reg.Signature,
			Message:               reg.Message,
			RepresentativeAddress: representativeAddress,
		})
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return res, err
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", s.cfg.Url.String()+BatchGenerateSignaturePath, bytes.NewReader(payloadBytes))
	if err != nil {
		return res, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return res, err
	}

	if resp.StatusCode != 200 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return res, fmt.Errorf("error reading invalid response (%d) body: %v", resp.StatusCode, err)
		}

		return res, fmt.Errorf("invalid response (%d): %v", resp.StatusCode, string(body))
	}

	var response BatchSignatureSwapResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return res, err
	}

	// ensure the length of original data matches the length of signatures
	if len(response.OriginalData) != len(response.EcdsaSignatures) {
		return res, fmt.Errorf("invalid response: length of original data does not match the length of signatures")
	}

	for i, reg := range response.OriginalData {
		res[reg.Message.Pubkey] = response.EcdsaSignatures[i]
	}

	return res, nil
}
