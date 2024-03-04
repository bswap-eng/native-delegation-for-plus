package beacon

import (
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/bswap-eng/native-delegation-for-plus/beacon/config"
)

type BeaconService struct {
	cfg    config.BeaconConfig
	client *http.Client

	mu sync.Mutex

	currentSlot uint64
}

func NewBeaconService() *BeaconService {
	return &BeaconService{
		client: &http.Client{Timeout: 6 * time.Second}, // get a response in half a slot
	}
}

func (b *BeaconService) Configure(cfg config.BeaconConfig) error {
	b.cfg = cfg

	err := b.connect(cfg.BeaconNodeUrl)
	if err != nil {
		return fmt.Errorf("failed to connect to beacon node: %w", err)
	}

	return nil
}

func (b *BeaconService) ConnectedChainId() *big.Int {
	return b.cfg.ChainID
}
