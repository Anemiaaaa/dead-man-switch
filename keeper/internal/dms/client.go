package dms

import (
	"context"
	"fmt"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Client reads vaults from an RPC node.
//
// Read-only by design: the keeper never signs anything. `claim` is
// permissionless and `check_in` is the owner's to make, so a keeper that could
// sign would be a private key sitting on a server for no reason.
type Client struct {
	rpc       *rpc.Client
	programID solana.PublicKey
}

func NewClient(endpoint string, programID solana.PublicKey) *Client {
	return &Client{rpc: rpc.New(endpoint), programID: programID}
}

// Vaults fetches every vault the program owns.
//
// Both filters matter: `DataSize` lets the node skip most accounts cheaply,
// and the discriminator memcmp keeps a future account type of the same size
// from reaching the decoder.
func (c *Client) Vaults(ctx context.Context) ([]*Vault, error) {
	accounts, err := c.rpc.GetProgramAccountsWithOpts(ctx, c.programID, &rpc.GetProgramAccountsOpts{
		Commitment: rpc.CommitmentConfirmed,
		Filters: []rpc.RPCFilter{
			{DataSize: AccountSize},
			{Memcmp: &rpc.RPCFilterMemcmp{Offset: 0, Bytes: AccountDiscriminator[:]}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dms: get program accounts: %w", err)
	}

	vaults := make([]*Vault, 0, len(accounts))
	for _, account := range accounts {
		vault, err := DecodeVault(account.Pubkey, account.Account.Data.GetBinary())
		if err != nil {
			// One malformed account must not blind the keeper to the rest.
			return nil, err
		}
		vault.Lamports = account.Account.Lamports
		vaults = append(vaults, vault)
	}

	return vaults, nil
}

// Vault fetches a single vault by address.
func (c *Client) Vault(ctx context.Context, address solana.PublicKey) (*Vault, error) {
	account, err := c.rpc.GetAccountInfoWithOpts(ctx, address, &rpc.GetAccountInfoOpts{
		Commitment: rpc.CommitmentConfirmed,
	})
	if err != nil {
		return nil, fmt.Errorf("dms: get account %s: %w", address, err)
	}
	if account == nil || account.Value == nil {
		return nil, fmt.Errorf("dms: vault %s does not exist", address)
	}

	vault, err := DecodeVault(address, account.Value.Data.GetBinary())
	if err != nil {
		return nil, err
	}
	vault.Lamports = account.Value.Lamports

	return vault, nil
}
