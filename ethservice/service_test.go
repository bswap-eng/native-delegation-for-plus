package ethservice_test

import (
	"net/url"
	"testing"

	"github.com/bswap-eng/native-delegation-for-plus/ethservice"
	"github.com/bswap-eng/native-delegation-for-plus/ethservice/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
)

func TestEthService_Connect(t *testing.T) {
	t.Log("TestEthService_Connect")

	newEthService := ethservice.NewEthService()

	nodeUrl, err := url.ParseRequestURI("https://gateway.tenderly.co/public/goerli")
	if err != nil {
		t.Fatal(err)
	}

	logger := logrus.NewEntry(logrus.New()).WithField("moduleExecution", "k2")

	ethServiceConfig := config.EthServiceConfig{
		ExecutionNodeUrl:                nodeUrl,
		ProposerRegistryContractAddress: common.HexToAddress("0x1643ec804d944Da97d90c013cBaCD1358Cce1bAF"),
		K2LendingContractAddress:        common.HexToAddress("0x10163A57EeCE9EB14Fe9e49889060D0E22c74F1F"),
	}

	err = newEthService.Configure(ethServiceConfig, logger)
	if err != nil {
		t.Fatal(err)
	}

}
