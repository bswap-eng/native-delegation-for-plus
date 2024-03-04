package balanceverifier

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

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/bswap-eng/native-delegation-for-plus/balanceverifier/config"
	k2Common "github.com/bswap-eng/native-delegation-for-plus/common"
)

type BalanceVerifierService struct {
	client *http.Client
	cfg    config.BalanceVerifierConfig
}

func NewBalanceVerifierService() *BalanceVerifierService {
	return &BalanceVerifierService{
		client: &http.Client{Timeout: 6 * time.Second}, // get a response in half a slot
	}
}

func (s *BalanceVerifierService) Configure(url *url.URL) error {

	if url == nil {
		return fmt.Errorf("balanceverifierservice: url not set, cannot configure service")
	}

	s.cfg.Url = url

	info, err := s.GetInfo()
	if err != nil {
		return fmt.Errorf("balanceverifierservice: failed to get info: %w", err)
	}

	s.cfg.ChainID = big.NewInt(int64(info.ChainID))

	return nil
}

func (s *BalanceVerifierService) ConnectedChainId() *big.Int {
	return s.cfg.ChainID
}

func (s *BalanceVerifierService) GetInfo() (Info, error) {
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

func (s *BalanceVerifierService) ReportEffectiveBalance(
	effectiveBalances map[phase0.BLSPubKey]uint64,
) (res map[phase0.BLSPubKey]k2Common.EcdsaSignature, err error) {

	payload := ReportEffectiveBalancePayload{}
	res = make(map[phase0.BLSPubKey]k2Common.EcdsaSignature)

	if len(effectiveBalances) == 0 {
		return res, nil
	}

	for pubkey, effectiveBalance := range effectiveBalances {
		payload.BLSPubKeys = append(payload.BLSPubKeys, pubkey)
		payload.EffectiveBalances = append(payload.EffectiveBalances, effectiveBalance)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return res, err
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", s.cfg.Url.String()+VerifyEffectiveBalancePath, bytes.NewReader(payloadBytes))
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

	var response ReportEffectiveBalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return res, err
	}

	// ensure the length of the response is the same as the length of the request
	if len(response.Responses) != len(effectiveBalances) {
		return res, fmt.Errorf("invalid response length: %d", len(response.Responses))
	}

	res = make(map[phase0.BLSPubKey]k2Common.EcdsaSignature)
	for _, item := range response.Responses {
		res[item.Report.BLSPubKey] = item.DesignatedVerifierSignature
	}

	return res, nil
}
