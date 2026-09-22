use anchor_lang::prelude::*;

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault, utils::move_lamports};

/// An heir takes their share of a native SOL vault.
///
/// Permissionless in the sense that matters: no keeper, no admin and no
/// co-heir has to act first. Each listed heir triggers their own payout the
/// moment the timer runs out, so the protocol keeps working even if the
/// off-chain service is dead and the other heirs never show up.
#[derive(Accounts)]
pub struct ClaimSol<'info> {
    #[account(mut)]
    pub beneficiary: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        constraint = vault.is_sol() @ DmsError::NotSolVault,
    )]
    pub vault: Account<'info, Vault>,
}

pub fn handle_claim_sol(ctx: Context<ClaimSol>) -> Result<()> {
    let now = Clock::get()?.unix_timestamp;
    let vault_info = ctx.accounts.vault.to_account_info();

    // Read straight from the sysvar inside this transaction. Trusting a cached
    // or client-supplied timestamp would open a race around a last-block
    // check-in.
    let available = Vault::withdrawable_lamports(&vault_info)?;
    let claimant = ctx.accounts.beneficiary.key();

    let vault = &mut ctx.accounts.vault;
    let ticket = vault.authorize_claim(&claimant, now, available)?;

    // The last heir sweeps the remainder so integer division cannot leave dust
    // behind in a vault that can never be reopened.
    let amount = if ticket.is_last {
        available
    } else {
        Vault::share_of(vault.claim_pool, ticket.share_bps)?
    };
    require!(amount > 0, DmsError::InsufficientFunds);
    require!(amount <= available, DmsError::InsufficientFunds);

    move_lamports(
        &vault_info,
        &ctx.accounts.beneficiary.to_account_info(),
        amount,
    )?;

    msg!(
        "{} claimed {} lamports ({} bps of a {} lamport pool)",
        claimant,
        amount,
        ticket.share_bps,
        ctx.accounts.vault.claim_pool
    );

    Ok(())
}
