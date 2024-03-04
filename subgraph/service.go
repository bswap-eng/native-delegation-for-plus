package subgraph

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/bswap-eng/native-delegation-for-plus/subgraph/config"
	"github.com/ethereum/go-ethereum/common"

	graphql "github.com/hasura/go-graphql-client"
)

type SubgraphService struct {
	client *graphql.Client
	cfg    config.SubgraphConfig
}

func NewSubgraphService() *SubgraphService {
	return &SubgraphService{}
}

func (s *SubgraphService) Configure(url *url.URL) error {

	if url == nil {
		return fmt.Errorf("subgraphservice: url not set, cannot configure service")
	}

	s.cfg.Url = url

	s.client = graphql.NewClient(url.String(), nil)

	return nil
}

func (s *SubgraphService) SetConnectedChainID(chainID *big.Int) {
	s.cfg.ChainID = chainID
}

// no need to lock as the setConfiguredChainID is called before the service is used
func (s *SubgraphService) ConnectedChainId() *big.Int {
	return s.cfg.ChainID
}

func (s *SubgraphService) MetaInfo() (response MetaQuery, err error) {

	if err := s.client.Query(context.Background(), &response, nil); err != nil {
		return response, fmt.Errorf("unable to retrieve subgraph meta data: %v", err)
	}

	return response, nil
}

func (s *SubgraphService) GetValidatorsByRepresentative(representatives []common.Address, limit uint64) (response NodeRunnerByIdsQuery, err error) {

	// if length of representatives is 0, return empty response
	if len(representatives) == 0 {
		return response, nil
	}

	if limit == 0 {
		limit = 1000 // max limit by default
		// max limit would cause recursive calls to ensure all bls keys are fetched
	}

	// if a custom limit is set that is less than the max limit, then we can make a single call to get the bls keys
	// for each representative
	// else if a custom limit is set that is greater than the max limit, then function will make recursive calls to get the bls keys
	// for each representative up to the limit

	return s.getValidatorsByRepresentative(representatives, 0, limit)

}

func (s *SubgraphService) getValidatorsByRepresentative(representatives []common.Address, skip_validators uint64, limit_validators uint64) (response NodeRunnerByIdsQuery, err error) {

	if len(representatives) > 1000 {
		// split the request into 2 recursive calls
		// this is to avoid the 1000 limit on the number of ids that can be passed to the subgraph

		result1, err := s.getValidatorsByRepresentative(representatives[:len(representatives)/2], skip_validators, limit_validators)
		if err != nil {
			return result1, err
		}
		result2, err := s.getValidatorsByRepresentative(representatives[len(representatives)/2:], skip_validators, limit_validators)
		if err != nil {
			return result2, err
		}
		response.NodeRunners = append(result1.NodeRunners, result2.NodeRunners...)

		return response, nil
	}

	ids := make([]string, 0, len(representatives))
	for _, representative := range representatives {
		ids = append(ids, strings.ToLower(representative.String()))
	}

	// if limit_validators is greater than max limit search, then we need to split the request into 2 recursive calls
	// as the user has requested a specific limit
	if limit_validators > 1000 {
		// split the request into 2 recursive calls
		result1, err := s.getValidatorsByRepresentative(representatives, skip_validators, limit_validators/2)
		if err != nil {
			return result1, err
		}

		result2, err := s.getValidatorsByRepresentative(representatives, skip_validators+(limit_validators/2), limit_validators/2)
		if err != nil {
			return result2, err
		}

		// merge the results into a single response by adding the bls keys from the second result to the first
		for i, nodeRunner1 := range result1.NodeRunners {
			nodeRunner1.BlsPublicKeys = append(nodeRunner1.BlsPublicKeys, result2.NodeRunners[i].BlsPublicKeys...)
			result1.NodeRunners[i] = nodeRunner1
		}

		return result1, nil
	}

	variables := map[string]any{
		"ids":              ids,
		"skip_validators":  skip_validators,
		"limit_validators": limit_validators,
	}

	if err := s.client.Query(context.Background(), &response, variables); err != nil {
		return response, fmt.Errorf("unable to retrieve subgraph data: %v", err)
	}

	// if the number of validators returned is equal to the limit, we need to make another call to get the next batch with the skip value incremented
	if limit_validators == 1000 { // if on max limit fetch then we need to make another call to get the next batch
		for i, nodeRunner := range response.NodeRunners {
			// if any node runner has bls public keys equal to the limit, we need to make another call to get the next batch
			if len(nodeRunner.BlsPublicKeys) == int(limit_validators) {
				// if on max limit fetch then we need to make another call to get the next batch
				// get the next batch of bls keys for the representative
				representative := []common.Address{nodeRunner.Id}
				nextBatch, err := s.getValidatorsByRepresentative(representative, skip_validators+limit_validators, limit_validators)
				if err != nil {
					return nextBatch, err
				}
				// append the next batch to the current node runner
				nodeRunner.BlsPublicKeys = append(nodeRunner.BlsPublicKeys, nextBatch.NodeRunners[0].BlsPublicKeys...)
				response.NodeRunners[i] = nodeRunner
			}
		}
	} // if not on max limit fetch then we don't need to make another call to get the next batch
	// as the user has requested a specific limit

	return response, nil
}
