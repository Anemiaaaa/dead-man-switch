use anchor_lang::prelude::*;

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault, utils::move_lamports};

/// Take lamports back out while the switch is still armed.
///
/// Allowed right up until the first claim actually lands, not merely until the
/// deadline passes: being late is not the same as being dead. An owner who
/// shows up after the deadline but before any heir has moved can still empty
/// the vault — and an heir who moves first wins. That race is the owner's to
/// avoid by checking in on time.
#[derive(Accounts)]
pub struct WithdrawSol<'info> {
    #[account(mut)]
    pub owner: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        has_one = owner @ DmsError::Unauthorized,
        constraint = vault.is_sol() @ DmsError::NotSolVault,
        constraint = !vault.is_claimed @ DmsError::VaultTriggered,
    )]
    pub vault: Account<'info, Vault>,
}

pub fn handle_withdraw_sol(ctx: Context<WithdrawSol>, amount: u64) -> Result<()> {
    require!(amount > 0, DmsError::InvalidAmount);

    let vault_info = ctx.accounts.vault.to_account_info();
    let available = Vault::withdrawable_lamports(&vault_info)?;
    require!(amount <= available, DmsError::InsufficientFunds);

    move_lamports(&vault_info, &ctx.accounts.owner.to_account_info(), amount)?;

    msg!(
        "withdrew {} lamports; {} still locked in the vault",
        amount,
        available - amount
    );

    Ok(())
}
