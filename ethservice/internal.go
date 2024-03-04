package ethservice

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/bswap-eng/native-delegation-for-plus/ethservice/contracts"
)

func (e *EthService) connect(url *url.URL) error {
	client, err := ethclient.Dial(url.String())
	if err != nil {
		return err
	}
	e.client = client
	e.cfg.ExecutionNodeUrl = url

	id, err := client.NetworkID(context.Background())
	if err != nil {
		return err
	}
	e.cfg.ChainID = id

	synced, err := client.SyncProgress(context.Background())
	if err != nil {
		return err
	}
	if synced != nil && synced.CurrentBlock != synced.HighestBlock {
		return fmt.Errorf("execution node not synced")
	}

	return nil
}

func (e *EthService) configureK2LendingContract(address common.Address) error {
	e.cfg.K2LendingContractAddress = address

	contractAbi, err := abi.JSON(strings.NewReader(contracts.K2_LENDING_CONTRACT_ABI))
	if err != nil {
		return fmt.Errorf("failed to parse k2 lending contract abi: %w", err)
	}
	e.cfg.K2LendingContractABI = &contractAbi

	// check if the contract is deployed
	contractByteCode, err := e.client.CodeAt(context.Background(), address, nil)
	if err != nil {
		return fmt.Errorf("failed to get k2 lending contract code: %w", err)
	}

	if len(contractByteCode) == 0 {
		return fmt.Errorf("k2 contract not deployed")
	}

	return nil
}

func (e *EthService) configureK2NodeOperatorContract(address common.Address) error {
	e.cfg.K2NodeOperatorContractAddress = address

	contractAbi, err := abi.JSON(strings.NewReader(contracts.K2_NODE_OPERATOR_CONTRACT_ABI))
	if err != nil {
		return fmt.Errorf("failed to parse k2 node operator contract abi: %w", err)
	}
	e.cfg.K2NodeOperatorContractABI = &contractAbi

	// check if the contract is deployed
	contractByteCode, err := e.client.CodeAt(context.Background(), address, nil)
	if err != nil {
		return fmt.Errorf("failed to get k2 node operator contract code: %w", err)
	}

	if len(contractByteCode) == 0 {
		return fmt.Errorf("k2 contract not deployed")
	}

	return nil
}

func (e *EthService) configureProposerRegistryContract(address common.Address) error {
	e.cfg.ProposerRegistryContractAddress = address

	contractAbi, err := abi.JSON(strings.NewReader(contracts.PROPOSER_REGISTRY_CONTRACT_ABI))
	if err != nil {
		return err
	}
	e.cfg.ProposerRegistryContractABI = &contractAbi

	contractByteCode, err := e.client.CodeAt(context.Background(), address, nil)
	if err != nil {
		return err
	}

	if len(contractByteCode) == 0 {
		return fmt.Errorf("proposer registry contract not deployed")
	}

	return nil
}

func (e *EthService) configureMulticallContract(address common.Address) error {
	e.cfg.MulticallContractAddress = address

	contractAbi, err := abi.JSON(strings.NewReader(contracts.MULTICALL3_CONTRACT_ABI))
	if err != nil {
		return fmt.Errorf("failed to parse multicall contract abi: %w", err)
	}
	e.cfg.MulticallContractABI = &contractAbi

	// check if the contract is deployed
	contractByteCode, err := e.client.CodeAt(context.Background(), address, nil)
	if err != nil {
		return fmt.Errorf("failed to get multicall contract code: %w", err)
	}

	if len(contractByteCode) == 0 {
		return fmt.Errorf("multicall contract not deployed")
	}

	return nil
}
