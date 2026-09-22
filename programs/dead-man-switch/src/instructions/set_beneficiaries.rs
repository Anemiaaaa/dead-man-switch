use anchor_lang::prelude::*;

use crate::{
    constants::{MAX_BENEFICIARIES, VAULT_SEED},
    error::DmsError,
    state::{Beneficiary, BeneficiaryInput, Vault},
};

/// Replaces the whole heir table in one shot.
///
/// This is deliberately *not* an `add_beneficiary` / `remove_beneficiary`
/// pair. Shares must always total exactly 100%, so adding or removing one heir
/// necessarily restates everyone else's share anyway — an "add" instruction
/// would either leave the vault in an invalid intermediate state or have to
/// guess how to rescale the others. Restating the table makes every edit
/// atomic and every stored table valid.
#[derive(Accounts)]
pub struct SetBeneficiaries<'info> {
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

pub fn handle_set_beneficiaries(
    ctx: Context<SetBeneficiaries>,
    beneficiaries: Vec<BeneficiaryInput>,
) -> Result<()> {
    require!(
        beneficiaries.len() <= MAX_BENEFICIARIES,
        DmsError::TooManyBeneficiaries
    );

    let count = beneficiaries.len();
    let mut slots = [Beneficiary::default(); MAX_BENEFICIARIES];
    for (slot, input) in slots.iter_mut().zip(beneficiaries.iter()) {
        *slot = Beneficiary {
            address: input.address,
            share_bps: input.share_bps,
            claimed: false,
        };
    }
    Vault::validate_slots(&slots[..count])?;

    let vault = &mut ctx.accounts.vault;
    vault.beneficiaries = slots;
    vault.beneficiary_count = count as u8;

    msg!("heir table replaced: {} heir(s)", count);

    Ok(())
}
