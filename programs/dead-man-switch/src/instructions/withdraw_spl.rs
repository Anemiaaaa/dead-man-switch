use anchor_lang::prelude::*;
use anchor_spl::{
    associated_token::AssociatedToken,
    token_interface::{transfer_checked, Mint, TokenAccount, TokenInterface, TransferChecked},
};

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// Take SPL tokens back out while the switch is still armed.
///
/// The vault's token account has the PDA as its authority, so this is the only
/// route back out — and the transfer is a CPI the PDA signs for. See
/// [`WithdrawSol`](super::WithdrawSol) on why the cut-off is the first claim
/// rather than the deadline.
#[derive(Accounts)]
pub struct WithdrawSpl<'info> {
    #[account(mut)]
    pub owner: Signer<'info>,

    #[account(
        mut,
        seeds = [VAULT_SEED, vault.owner.as_ref(), &vault.vault_id.to_le_bytes()],
        bump = vault.bump,
        has_one = owner @ DmsError::Unauthorized,
        constraint = !vault.is_sol() @ DmsError::NotSplVault,
        constraint = !vault.is_claimed @ DmsError::VaultTriggered,
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

    #[account(
        init_if_needed,
        payer = owner,
        associated_token::mint = mint,
        associated_token::authority = owner,
        associated_token::token_program = token_program,
    )]
    pub owner_token_account: InterfaceAccount<'info, TokenAccount>,

    pub token_program: Interface<'info, TokenInterface>,
    pub associated_token_program: Program<'info, AssociatedToken>,
    pub system_program: Program<'info, System>,
}

pub fn handle_withdraw_spl(ctx: Context<WithdrawSpl>, amount: u64) -> Result<()> {
    require!(amount > 0, DmsError::InvalidAmount);
    require!(
        amount <= ctx.accounts.vault_token_account.amount,
        DmsError::InsufficientFunds
    );

    let owner_key = ctx.accounts.vault.owner;
    let vault_id = ctx.accounts.vault.vault_id.to_le_bytes();
    let bump = [ctx.accounts.vault.bump];
    let seeds: &[&[u8]] = &[VAULT_SEED, owner_key.as_ref(), &vault_id, &bump];
    let signer_seeds = &[seeds];

    let cpi_accounts = TransferChecked {
        from: ctx.accounts.vault_token_account.to_account_info(),
        mint: ctx.accounts.mint.to_account_info(),
        to: ctx.accounts.owner_token_account.to_account_info(),
        authority: ctx.accounts.vault.to_account_info(),
    };
    transfer_checked(
        CpiContext::new_with_signer(ctx.accounts.token_program.key(), cpi_accounts, signer_seeds),
        amount,
        ctx.accounts.mint.decimals,
    )?;

    msg!("withdrew {} tokens back to the owner", amount);

    Ok(())
}
