package watcher

import (
	"context"
	"fmt"
	"time"

	"github.com/scroll-tech/go-ethereum/log"
	"gorm.io/gorm"

	"scroll-tech/bridge/internal/config"
	"scroll-tech/bridge/internal/orm"
	bridgeTypes "scroll-tech/bridge/internal/types"
)

// ChunkProposer proposes chunks based on available unchunked blocks.
type ChunkProposer struct {
	ctx context.Context
	db  *gorm.DB

	chunkOrm   *orm.Chunk
	l2BlockOrm *orm.L2Block

	maxTxGasPerChunk                uint64
	maxL2TxNumPerChunk              uint64
	maxL1CommitGasPerChunk          uint64
	maxL1CommitCalldataSizePerChunk uint64
	minL1CommitCalldataSizePerChunk uint64
	chunkTimeoutSec                 uint64
}

// NewChunkProposer creates a new ChunkProposer instance.
func NewChunkProposer(ctx context.Context, cfg *config.ChunkProposerConfig, db *gorm.DB) *ChunkProposer {
	return &ChunkProposer{
		ctx:                             ctx,
		db:                              db,
		chunkOrm:                        orm.NewChunk(db),
		l2BlockOrm:                      orm.NewL2Block(db),
		maxTxGasPerChunk:                cfg.MaxTxGasPerChunk,
		maxL2TxNumPerChunk:              cfg.MaxL2TxNumPerChunk,
		maxL1CommitGasPerChunk:          cfg.MaxL1CommitGasPerChunk,
		maxL1CommitCalldataSizePerChunk: cfg.MaxL1CommitCalldataSizePerChunk,
		minL1CommitCalldataSizePerChunk: cfg.MinL1CommitCalldataSizePerChunk,
		chunkTimeoutSec:                 cfg.ChunkTimeoutSec,
	}
}

// TryProposeChunk tries to propose a new chunk.
func (p *ChunkProposer) TryProposeChunk() {
	proposedChunk, err := p.proposeChunk()
	if err != nil {
		log.Error("propose new chunk failed", "err", err)
		return
	}

	if err := p.updateChunkInfoInDB(proposedChunk); err != nil {
		log.Error("update chunk info in orm failed", "err", err)
	}
}

func (p *ChunkProposer) updateChunkInfoInDB(chunk *bridgeTypes.Chunk) error {
	if chunk == nil {
		log.Warn("proposed chunk is nil, cannot update in DB")
		return nil
	}

	err := p.db.Transaction(func(dbTX *gorm.DB) error {
		dbChunk, err := p.chunkOrm.InsertChunk(p.ctx, chunk, dbTX)
		if err != nil {
			return err
		}
		if err := p.l2BlockOrm.UpdateChunkHashInRange(p.ctx, dbChunk.StartBlockNumber, dbChunk.EndBlockNumber, dbChunk.Hash, dbTX); err != nil {
			log.Error("failed to update chunk_hash for l2_blocks", "chunk hash", chunk.Hash, "start block", 0, "end block", 0, "err", err)
			return err
		}
		return nil
	})
	return err
}

func (p *ChunkProposer) proposeChunk() (*bridgeTypes.Chunk, error) {
	blocks, err := p.l2BlockOrm.GetUnchunkedBlocks(p.ctx)
	if err != nil {
		return nil, err
	}

	if len(blocks) == 0 {
		log.Warn("no un-chunked blocks")
		return nil, nil
	}

	firstBlock := blocks[0]
	totalTxGasUsed := firstBlock.Header.GasUsed
	totalL2TxNum := firstBlock.L2TxsNum()
	totalL1CommitCalldataSize := firstBlock.EstimateL1CommitCalldataSize()
	totalL1CommitGas := firstBlock.EstimateL1CommitGas()

	// Check if the first block breaks hard limits.
	// If so, it indicates there are bugs in sequencer, manual fix is needed.
	if totalL2TxNum > p.maxL2TxNumPerChunk {
		return nil, fmt.Errorf(
			"the first block exceeds l2 tx number limit; block number: %v, number of transactions: %v, max transaction number limit: %v",
			firstBlock.Header.Number,
			totalL2TxNum,
			p.maxL2TxNumPerChunk,
		)
	}

	if totalTxGasUsed > p.maxTxGasPerChunk {
		return nil, fmt.Errorf(
			"the first block exceeds l2 tx gas limit; block number: %v, gas used: %v, max gas limit: %v",
			firstBlock.Header.Number,
			totalTxGasUsed,
			p.maxTxGasPerChunk,
		)
	}

	if totalL1CommitGas > p.maxL1CommitGasPerChunk {
		return nil, fmt.Errorf(
			"the first block exceeds l1 commit gas limit; block number: %v, commit gas: %v, max commit gas limit: %v",
			firstBlock.Header.Number,
			totalL1CommitGas,
			p.maxL1CommitGasPerChunk,
		)
	}

	if totalL1CommitCalldataSize > p.maxL1CommitCalldataSizePerChunk {
		return nil, fmt.Errorf(
			"the first block exceeds l1 commit calldata size limit; block number: %v, calldata size: %v, max calldata size limit: %v",
			firstBlock.Header.Number,
			totalL1CommitCalldataSize,
			p.maxL1CommitCalldataSizePerChunk,
		)
	}

	for i, block := range blocks[1:] {
		totalTxGasUsed += block.Header.GasUsed
		totalL2TxNum += block.L2TxsNum()
		totalL1CommitCalldataSize += block.EstimateL1CommitCalldataSize()
		totalL1CommitGas += block.EstimateL1CommitGas()
		if totalTxGasUsed > p.maxTxGasPerChunk ||
			totalL2TxNum > p.maxL2TxNumPerChunk ||
			totalL1CommitCalldataSize > p.maxL1CommitCalldataSizePerChunk ||
			totalL1CommitGas > p.maxL1CommitGasPerChunk {
			blocks = blocks[:i+1]
			break
		}
	}

	var hasBlockTimeout bool
	currentTimeSec := uint64(time.Now().Unix())
	if blocks[0].Header.Time+p.chunkTimeoutSec < currentTimeSec {
		log.Warn("first block timeout",
			"block number", blocks[0].Header.Number,
			"block timestamp", blocks[0].Header.Time,
			"block outdated time threshold", currentTimeSec,
		)
		hasBlockTimeout = true
	}

	if !hasBlockTimeout && totalL1CommitCalldataSize < p.minL1CommitCalldataSizePerChunk {
		log.Warn("The calldata size of the chunk is less than the minimum limit",
			"totalL1CommitCalldataSize", totalL1CommitCalldataSize,
			"minL1CommitCalldataSizePerChunk", p.minL1CommitCalldataSizePerChunk,
		)
		return nil, nil
	}
	return &bridgeTypes.Chunk{Blocks: blocks}, nil
}