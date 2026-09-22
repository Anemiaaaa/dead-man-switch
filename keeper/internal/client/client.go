// Package client builds and sends Dead Man's Switch instructions.
//
// [Instructions] is key-less and shared; [Client] is the part that signs, and
// it belongs to the CLI and to nothing that runs unattended. Nothing under
// `internal/watch` imports this package.
package client

import (
	"context"
	"fmt"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Client signs and sends on behalf of one keypair.
type Client struct {
	rpc   *rpc.Client
	build Instructions
	payer solana.PrivateKey
}

func New(endpoint string, programID solana.PublicKey, payer solana.PrivateKey) *Client {
	return &Client{
		rpc:   rpc.New(endpoint),
		build: NewInstructions(programID),
		payer: payer,
	}
}

func (c *Client) Payer() solana.PublicKey { return c.payer.PublicKey() }

// VaultAddress is the PDA for one of the payer's vaults.
func (c *Client) VaultAddress(vaultID uint64) (solana.PublicKey, error) {
	return c.build.VaultAddress(c.payer.PublicKey(), vaultID)
}

// InitializeVault opens a native SOL vault and returns its address.
func (c *Client) InitializeVault(
	ctx context.Context,
	vaultID uint64,
	timeout time.Duration,
	heirs []Heir,
) (solana.PublicKey, solana.Signature, error) {
	vault, err := c.VaultAddress(vaultID)
	if err != nil {
		return solana.PublicKey{}, solana.Signature{}, err
	}

	signature, err := c.send(ctx,
		c.build.InitializeVault(c.payer.PublicKey(), vault, vaultID, timeout, heirs))

	return vault, signature, err
}

func (c *Client) DepositSOL(ctx context.Context, vault solana.PublicKey, lamports uint64) (solana.Signature, error) {
	return c.send(ctx, c.build.DepositSOL(c.payer.PublicKey(), vault, lamports))
}

func (c *Client) WithdrawSOL(ctx context.Context, vault solana.PublicKey, lamports uint64) (solana.Signature, error) {
	return c.send(ctx, c.build.WithdrawSOL(c.payer.PublicKey(), vault, lamports))
}

func (c *Client) CheckIn(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, c.build.CheckIn(c.payer.PublicKey(), vault))
}

func (c *Client) ClaimSOL(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, c.build.ClaimSOL(c.payer.PublicKey(), vault))
}

func (c *Client) CloseVault(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, c.build.CloseVault(c.payer.PublicKey(), vault))
}

func (c *Client) send(ctx context.Context, instruction solana.Instruction) (solana.Signature, error) {
	blockhash, err := c.rpc.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return solana.Signature{}, fmt.Errorf("client: latest blockhash: %w", err)
	}

	tx, err := solana.NewTransaction(
		[]solana.Instruction{instruction},
		blockhash.Value.Blockhash,
		solana.TransactionPayer(c.payer.PublicKey()),
	)
	if err != nil {
		return solana.Signature{}, fmt.Errorf("client: building transaction: %w", err)
	}

	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(c.payer.PublicKey()) {
			return &c.payer
		}
		return nil
	}); err != nil {
		return solana.Signature{}, fmt.Errorf("client: signing: %w", err)
	}

	signature, err := c.rpc.SendTransactionWithOpts(ctx, tx, rpc.TransactionOpts{
		PreflightCommitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return solana.Signature{}, fmt.Errorf("client: sending: %w", err)
	}

	return signature, c.confirm(ctx, signature)
}

// confirm polls rather than opening a websocket: one subscription per CLI
// invocation would cost more than it saves.
//
// A failed poll is retried rather than returned. The transaction is already in
// flight by this point, so a rate-limited status check says nothing about
// whether it landed — giving up here would report a failure for a transaction
// that actually succeeded, which is the worst answer available.
func (c *Client) confirm(ctx context.Context, signature solana.Signature) error {
	const pollInterval = 2 * time.Second

	deadline := time.Now().Add(90 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		statuses, err := c.rpc.GetSignatureStatuses(ctx, true, signature)
		switch {
		case err != nil:
			lastErr = err
		case len(statuses.Value) > 0 && statuses.Value[0] != nil:
			status := statuses.Value[0]
			if status.Err != nil {
				return fmt.Errorf("client: transaction %s failed on-chain: %v", signature, status.Err)
			}
			if status.ConfirmationStatus == rpc.ConfirmationStatusConfirmed ||
				status.ConfirmationStatus == rpc.ConfirmationStatusFinalized {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}

	if lastErr != nil {
		return fmt.Errorf("client: could not confirm %s (it may still have landed): %w", signature, lastErr)
	}

	return fmt.Errorf("client: transaction %s was not confirmed in time", signature)
}
