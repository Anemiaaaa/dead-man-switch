use anchor_lang::{prelude::*, system_program};

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// Fund a native SOL vault.
///
/// Lamports land on the vault PDA itself, which is what locks them: the PDA is
/// owned by this program, so nothing but this program can move them out again.
#[derive(Accounts)]
pub struct DepositSol<'info> {
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

    pub system_program: Program<'info, System>,
}

pub fn handle_deposit_sol(ctx: Context<DepositSol>, amount: u64) -> Result<()> {
    require!(amount > 0, DmsError::InvalidAmount);

    // The owner is a plain system account, so a System Program transfer is the
    // right tool here; only the way *out* of the vault needs PDA signing.
    let cpi_accounts = system_program::Transfer {
        from: ctx.accounts.owner.to_account_info(),
        to: ctx.accounts.vault.to_account_info(),
    };
    system_program::transfer(CpiContext::new(system_program::ID, cpi_accounts), amount)?;

    let vault_info = ctx.accounts.vault.to_account_info();
    msg!(
        "deposited {} lamports; vault now holds {} claimable lamports",
        amount,
        Vault::withdrawable_lamports(&vault_info)?
    );

    Ok(())
}
