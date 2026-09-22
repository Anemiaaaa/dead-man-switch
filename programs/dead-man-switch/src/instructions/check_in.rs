use anchor_lang::prelude::*;

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// "I'm alive" — pushes the deadline out by another full timeout.
///
/// Only the owner may call it. If an heir could reset the timer they could
/// stall their co-heirs indefinitely; if they could shorten it they could
/// force an early payout.
#[derive(Accounts)]
pub struct CheckIn<'info> {
    pub owner: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        has_one = owner @ DmsError::Unauthorized,
        constraint = !vault.is_claimed @ DmsError::VaultTriggered,
    )]
    pub vault: Account<'info, Vault>,
}

pub fn handle_check_in(ctx: Context<CheckIn>) -> Result<()> {
    let now = Clock::get()?.unix_timestamp;
    let vault = &mut ctx.accounts.vault;
    vault.last_checkin = now;

    msg!(
        "check-in at {}; heirs unlocked from {}",
        now,
        vault.deadline()?
    );

    Ok(())
}
