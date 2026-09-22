use anchor_lang::prelude::*;
use anchor_spl::{
    associated_token::AssociatedToken,
    token_interface::{transfer_checked, Mint, TokenAccount, TokenInterface, TransferChecked},
};

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// An heir takes their share of an SPL token vault.
///
/// Mirrors [`ClaimSol`](super::ClaimSol); the rules about who may claim what
/// live in [`Vault::authorize_claim`] so both paths cannot drift apart.
#[derive(Accounts)]
pub struct ClaimSpl<'info> {
    #[account(mut)]
    pub beneficiary: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        constraint = !vault.is_sol() @ DmsError::NotSplVault,
    )]
    pub vault: Account<'info, Vault>,

    #[account(
        constraint = vault.mint == Some(mint.key()) @ DmsError::MintMismatch,
    )]
    pub mint: InterfaceAccount<'info, Mint>,

    #[account(
        mut,
        associated_token::mint = mint,
        associated_token::authority = vault,
        associated_token::token_program = token_program,
    )]
    pub vault_token_account: InterfaceAccount<'info, TokenAccount>,

    /// Created on the fly if the heir has never held this token before —
    /// otherwise inheriting would require the heir to prepare an account for a
    /// token they may not know exists.
    #[account(
        init_if_needed,
        payer = beneficiary,
        associated_token::mint = mint,
        associated_token::authority = beneficiary,
        associated_token::token_program = token_program,
    )]
    pub beneficiary_token_account: InterfaceAccount<'info, TokenAccount>,

    pub token_program: Interface<'info, TokenInterface>,
    pub associated_token_program: Program<'info, AssociatedToken>,
    pub system_program: Program<'info, System>,
}

pub fn handle_claim_spl(ctx: Context<ClaimSpl>) -> Result<()> {
    let now = Clock::get()?.unix_timestamp;
    let available = ctx.accounts.vault_token_account.amount;
    let claimant = ctx.accounts.beneficiary.key();

    let ticket = ctx
        .accounts
        .vault
        .authorize_claim(&claimant, now, available)?;

    let amount = if ticket.is_last {
        available
    } else {
        Vault::share_of(ctx.accounts.vault.claim_pool, ticket.share_bps)?
    };
    require!(amount > 0, DmsError::InsufficientFunds);
    require!(amount <= available, DmsError::InsufficientFunds);

    let owner_key = ctx.accounts.vault.owner;
    let vault_id = ctx.accounts.vault.vault_id.to_le_bytes();
    let bump = [ctx.accounts.vault.bump];
    let seeds: &[&[u8]] = &[VAULT_SEED, owner_key.as_ref(), &vault_id, &bump];
    let signer_seeds = &[seeds];

    let cpi_accounts = TransferChecked {
        from: ctx.accounts.vault_token_account.to_account_info(),
        mint: ctx.accounts.mint.to_account_info(),
        to: ctx.accounts.beneficiary_token_account.to_account_info(),
        authority: ctx.accounts.vault.to_account_info(),
    };
    transfer_checked(
        CpiContext::new_with_signer(ctx.accounts.token_program.key(), cpi_accounts, signer_seeds),
        amount,
        ctx.accounts.mint.decimals,
    )?;

    msg!(
        "{} claimed {} tokens ({} bps of a {} token pool)",
        claimant,
        amount,
        ticket.share_bps,
        ctx.accounts.vault.claim_pool
    );

    Ok(())
}
