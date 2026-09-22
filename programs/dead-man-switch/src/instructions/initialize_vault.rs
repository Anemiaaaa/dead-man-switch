use anchor_lang::prelude::*;
use anchor_spl::token_interface::Mint;

use crate::{
    constants::{MAX_BENEFICIARIES, MAX_TIMEOUT_SECONDS, MIN_TIMEOUT_SECONDS, VAULT_SEED},
    error::DmsError,
    state::{Beneficiary, BeneficiaryInput, Vault},
};

#[derive(Accounts)]
#[instruction(vault_id: u64)]
pub struct InitializeVault<'info> {
    #[account(mut)]
    pub owner: Signer<'info>,

    #[account(
        init,
        payer = owner,
        space = 8 + Vault::INIT_SPACE,
        seeds = [VAULT_SEED, owner.key().as_ref(), &vault_id.to_le_bytes()],
        bump,
    )]
    pub vault: Account<'info, Vault>,

    /// Mint this vault will hold. Omit it for a native SOL vault.
    ///
    /// Taking the real mint account (rather than a bare pubkey argument) means
    /// the program refuses to create a vault pointed at something that is not
    /// a mint at all.
    pub mint: Option<InterfaceAccount<'info, Mint>>,

    pub system_program: Program<'info, System>,
}

pub fn handle_initialize_vault(
    ctx: Context<InitializeVault>,
    vault_id: u64,
    timeout_seconds: i64,
    beneficiaries: Vec<BeneficiaryInput>,
) -> Result<()> {
    require!(
        (MIN_TIMEOUT_SECONDS..=MAX_TIMEOUT_SECONDS).contains(&timeout_seconds),
        DmsError::InvalidTimeout
    );
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

    let now = Clock::get()?.unix_timestamp;
    let vault = &mut ctx.accounts.vault;
    vault.owner = ctx.accounts.owner.key();
    vault.vault_id = vault_id;
    vault.mint = ctx.accounts.mint.as_ref().map(|mint| mint.key());
    vault.last_checkin = now;
    vault.timeout_seconds = timeout_seconds;
    vault.beneficiaries = slots;
    vault.beneficiary_count = count as u8;
    vault.is_claimed = false;
    vault.claim_pool = 0;
    vault.bump = ctx.bumps.vault;

    msg!(
        "vault {} opened: {} heir(s), timeout {}s, deadline {}",
        vault.key(),
        count,
        timeout_seconds,
        vault.deadline()?
    );

    Ok(())
}
