// Package client builds and sends Dead Man's Switch instructions.
//
// Kept apart from the keeper on purpose. The keeper reads the chain and holds
// no keys; this package signs, so it belongs to the CLI and to nothing that
// runs unattended. Nothing under `internal/watch` imports it.
//
// The discriminators below come from the generated IDL rather than from a hash
// computed here, so a rename in the program shows up as a failing transaction
// against a stale constant instead of a silently different instruction.
package client

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"

	"github.com/Anemiaaaa/dead-man-switch/keeper/internal/dms"
)

var (
	ixInitializeVault = [8]byte{48, 191, 163, 44, 71, 129, 63, 164}
	ixDepositSOL      = [8]byte{108, 81, 78, 117, 125, 155, 56, 200}
	ixCheckIn         = [8]byte{209, 253, 4, 217, 250, 241, 207, 50}
	ixWithdrawSOL     = [8]byte{145, 131, 74, 136, 65, 137, 42, 38}
	ixClaimSOL        = [8]byte{139, 113, 179, 189, 190, 30, 132, 195}
	ixCloseVault      = [8]byte{141, 103, 17, 126, 72, 75, 29, 29}
)

// Heir is one entry of the table passed to `initialize_vault`.
type Heir struct {
	Address  solana.PublicKey
	ShareBPS uint16
}

// Client signs and sends on behalf of one keypair.
type Client struct {
	rpc       *rpc.Client
	programID solana.PublicKey
	payer     solana.PrivateKey
}

func New(endpoint string, programID solana.PublicKey, payer solana.PrivateKey) *Client {
	return &Client{rpc: rpc.New(endpoint), programID: programID, payer: payer}
}

func (c *Client) Payer() solana.PublicKey { return c.payer.PublicKey() }

// VaultAddress is the PDA for one of the payer's vaults.
func (c *Client) VaultAddress(vaultID uint64) (solana.PublicKey, error) {
	address, _, err := dms.FindVaultAddress(c.programID, c.payer.PublicKey(), vaultID)
	return address, err
}

// InitializeVault opens a native SOL vault.
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

	data := args(ixInitializeVault)
	data = binary.LittleEndian.AppendUint64(data, vaultID)
	data = binary.LittleEndian.AppendUint64(data, uint64(timeout/time.Second))
	// Borsh encodes a Vec as a u32 length followed by the items.
	data = binary.LittleEndian.AppendUint32(data, uint32(len(heirs)))
	for _, heir := range heirs {
		data = append(data, heir.Address.Bytes()...)
		data = binary.LittleEndian.AppendUint16(data, heir.ShareBPS)
	}

	instruction := solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		// Anchor's wire convention for an omitted optional account is the
		// program's own id in that slot — this is what makes it a SOL vault.
		{PublicKey: c.programID},
		{PublicKey: solana.SystemProgramID},
	}, data)

	signature, err := c.send(ctx, instruction)

	return vault, signature, err
}

// DepositSOL funds a native vault.
func (c *Client) DepositSOL(ctx context.Context, vault solana.PublicKey, lamports uint64) (solana.Signature, error) {
	data := binary.LittleEndian.AppendUint64(args(ixDepositSOL), lamports)

	return c.send(ctx, solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		{PublicKey: solana.SystemProgramID},
	}, data))
}

// WithdrawSOL takes lamports back out while the switch is still armed.
func (c *Client) WithdrawSOL(ctx context.Context, vault solana.PublicKey, lamports uint64) (solana.Signature, error) {
	data := binary.LittleEndian.AppendUint64(args(ixWithdrawSOL), lamports)

	return c.send(ctx, solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
	}, data))
}

// CheckIn resets the timer.
func (c *Client) CheckIn(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true},
		{PublicKey: vault, IsWritable: true},
	}, args(ixCheckIn)))
}

// ClaimSOL takes the payer's share of an expired vault.
func (c *Client) ClaimSOL(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
	}, args(ixClaimSOL)))
}

// CloseVault closes an empty native vault and returns its rent.
func (c *Client) CloseVault(ctx context.Context, vault solana.PublicKey) (solana.Signature, error) {
	return c.send(ctx, solana.NewInstruction(c.programID, solana.AccountMetaSlice{
		{PublicKey: c.payer.PublicKey(), IsSigner: true, IsWritable: true},
		{PublicKey: vault, IsWritable: true},
		// Both optional accounts omitted: a SOL vault has no token account.
		{PublicKey: c.programID},
		{PublicKey: c.programID},
	}, args(ixCloseVault)))
}

// args starts an instruction payload with its discriminator.
func args(discriminator [8]byte) []byte {
	out := make([]byte, 0, 64)
	return append(out, discriminator[:]...)
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
