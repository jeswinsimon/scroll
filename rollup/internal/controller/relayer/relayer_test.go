package relayer

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"os"
	"strconv"
	"testing"

	"github.com/scroll-tech/go-ethereum/common"
	"github.com/scroll-tech/go-ethereum/ethclient"
	"github.com/scroll-tech/go-ethereum/log"
	"github.com/stretchr/testify/assert"

	"scroll-tech/common/database"
	"scroll-tech/common/docker"
	dockercompose "scroll-tech/common/docker-compose/l1"
	"scroll-tech/common/types/encoding"
	"scroll-tech/common/types/encoding/codecv0"

	"scroll-tech/rollup/internal/config"
)

var (
	// config
	cfg *config.Config

	base         *docker.App
	posL1TestEnv *dockercompose.PoSL1TestEnv

	// l2geth client
	l2Cli *ethclient.Client

	// l2 block
	block1 *encoding.Block
	block2 *encoding.Block

	// chunk
	chunk1     *encoding.Chunk
	chunk2     *encoding.Chunk
	chunkHash1 common.Hash
	chunkHash2 common.Hash
)

func setupEnv(t *testing.T) {
	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.LogfmtFormat()))
	glogger.Verbosity(log.LvlInfo)
	log.Root().SetHandler(glogger)

	// Load config.
	var err error
	cfg, err = config.NewConfig("../../../conf/config.json")
	assert.NoError(t, err)

	base.RunL2Geth(t)
	base.RunDBImage(t)

	cfg.L2Config.RelayerConfig.SenderConfig.Endpoint = posL1TestEnv.Endpoint()
	cfg.L1Config.RelayerConfig.SenderConfig.Endpoint = base.L2gethImg.Endpoint()
	cfg.DBConfig = &database.Config{
		DSN:         base.DBConfig.DSN,
		DriverName:  base.DBConfig.DriverName,
		MaxOpenNum:  base.DBConfig.MaxOpenNum,
		MaxIdleNum:  base.DBConfig.MaxIdleNum,
		MaxLifetime: base.DBConfig.MaxLifetime,
		MaxIdleTime: base.DBConfig.MaxIdleTime,
	}
	port, err := rand.Int(rand.Reader, big.NewInt(10000))
	assert.NoError(t, err)
	svrPort := strconv.FormatInt(port.Int64()+50000, 10)
	cfg.L2Config.RelayerConfig.ChainMonitor.BaseURL = "http://localhost:" + svrPort

	// Create l2geth client.
	l2Cli, err = base.L2Client()
	assert.NoError(t, err)

	templateBlockTrace1, err := os.ReadFile("../../../testdata/blockTrace_02.json")
	assert.NoError(t, err)
	block1 = &encoding.Block{}
	err = json.Unmarshal(templateBlockTrace1, block1)
	assert.NoError(t, err)
	chunk1 = &encoding.Chunk{Blocks: []*encoding.Block{block1}}
	daChunk1, err := codecv0.NewDAChunk(chunk1, 0)
	assert.NoError(t, err)
	chunkHash1, err = daChunk1.Hash()
	assert.NoError(t, err)

	templateBlockTrace2, err := os.ReadFile("../../../testdata/blockTrace_03.json")
	assert.NoError(t, err)
	block2 = &encoding.Block{}
	err = json.Unmarshal(templateBlockTrace2, block2)
	assert.NoError(t, err)
	chunk2 = &encoding.Chunk{Blocks: []*encoding.Block{block2}}
	daChunk2, err := codecv0.NewDAChunk(chunk2, chunk1.NumL1Messages(0))
	assert.NoError(t, err)
	chunkHash2, err = daChunk2.Hash()
	assert.NoError(t, err)
}

func TestMain(m *testing.M) {
	base = docker.NewDockerApp()
	base.Free()

	var err error
	posL1TestEnv, err = dockercompose.NewPoSL1TestEnv()
	if err != nil {
		log.Crit("failed to create PoS L1 test environment", "err", err)
	}
	if err := posL1TestEnv.Start(); err != nil {
		log.Crit("failed to start PoS L1 test environment", "err", err)
	}
	defer posL1TestEnv.Stop()

	m.Run()
}

func TestFunctions(t *testing.T) {
	setupEnv(t)
	srv, err := mockChainMonitorServer(cfg.L2Config.RelayerConfig.ChainMonitor.BaseURL)
	assert.NoError(t, err)
	defer srv.Close()

	// Run l1 relayer test cases.
	t.Run("TestCreateNewL1Relayer", testCreateNewL1Relayer)
	t.Run("TestL1RelayerGasOracleConfirm", testL1RelayerGasOracleConfirm)
	t.Run("TestL1RelayerProcessGasPriceOracle", testL1RelayerProcessGasPriceOracle)

	// Run l2 relayer test cases.
	t.Run("TestCreateNewRelayer", testCreateNewRelayer)
	t.Run("TestL2RelayerProcessPendingBatches", testL2RelayerProcessPendingBatches)
	t.Run("TestL2RelayerProcessCommittedBatches", testL2RelayerProcessCommittedBatches)
	t.Run("TestL2RelayerFinalizeTimeoutBatches", testL2RelayerFinalizeTimeoutBatches)
	t.Run("TestL2RelayerCommitConfirm", testL2RelayerCommitConfirm)
	t.Run("TestL2RelayerFinalizeConfirm", testL2RelayerFinalizeConfirm)
	t.Run("TestL2RelayerGasOracleConfirm", testL2RelayerGasOracleConfirm)
	t.Run("TestLayer2RelayerProcessGasPriceOracle", testLayer2RelayerProcessGasPriceOracle)
	// test getBatchStatusByIndex
	t.Run("TestGetBatchStatusByIndex", testGetBatchStatusByIndex)
}
