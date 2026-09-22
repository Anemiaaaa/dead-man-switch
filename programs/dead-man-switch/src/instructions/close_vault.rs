use anchor_lang::prelude::*;
use anchor_spl::token_interface::{close_account, CloseAccount, TokenAccount, TokenInterface};

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// Close an empty vault and hand the rent back to the owner.
///
/// Works both before anything happened and after every heir has claimed — but
/// only once nothing of value is left, so no balance is ever orphaned by
/// closing the account that describes it.
#[derive(Accounts)]
pub struct CloseVault<'info> {
    #[account(mut)]
    pub owner: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        has_one = owner @ DmsError::Unauthorized,
        close = owner,
    )]
    pub vault: Account<'info, Vault>,

    /// Required for an SPL vault, omitted for a native SOL one.
    ///
    /// It is closed along with the vault, so the rent locked in it goes back
    /// to the owner too rather than sitting in an account nobody can reach.
    #[account(mut)]
    pub vault_token_account: Option<InterfaceAccount<'info, TokenAccount>>,

    pub token_program: Option<Interface<'info, TokenInterface>>,
}

pub fn handle_close_vault(ctx: Context<CloseVault>) -> Result<()> {
    let vault_info = ctx.accounts.vault.to_account_info();
    let vault_key = ctx.accounts.vault.key();

    match ctx.accounts.vault.mint {
        None => {
            require!(
                Vault::withdrawable_lamports(&vault_info)? == 0,
                DmsError::VaultNotEmpty
            );
        }
        Some(mint) => {
            let token_account = ctx
                .accounts
                .vault_token_account
                .as_ref()
                .ok_or_else(|| error!(DmsError::MissingTokenAccount))?;
            let token_program = ctx
                .accounts
                .token_program
                .as_ref()
                .ok_or_else(|| error!(DmsError::MissingTokenAccount))?;

            require_keys_eq!(token_account.mint, mint, DmsError::MintMismatch);
            require_keys_eq!(token_account.owner, vault_key, DmsError::Unauthorized);
            require!(token_account.amount == 0, DmsError::VaultNotEmpty);

            let owner_key = ctx.accounts.vault.owner;
            let vault_id = ctx.accounts.vault.vault_id.to_le_bytes();
            let bump = [ctx.accounts.vault.bump];
            let seeds: &[&[u8]] = &[VAULT_SEED, owner_key.as_ref(), &vault_id, &bump];
            let signer_seeds = &[seeds];

            let cpi_accounts = CloseAccount {
                account: token_account.to_account_info(),
                destination: ctx.accounts.owner.to_account_info(),
                authority: vault_info.clone(),
            };
            close_account(CpiContext::new_with_signer(
                token_program.key(),
                cpi_accounts,
                signer_seeds,
            ))?;
        }
    }

    msg!("vault {} closed; rent returned to the owner", vault_key);

    Ok(())
}
