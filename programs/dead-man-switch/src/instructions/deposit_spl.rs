use anchor_lang::prelude::*;
use anchor_spl::{
    associated_token::AssociatedToken,
    token_interface::{transfer_checked, Mint, TokenAccount, TokenInterface, TransferChecked},
};

use crate::{constants::VAULT_SEED, error::DmsError, state::Vault};

/// Fund an SPL token vault.
///
/// Tokens go to an ATA whose authority is the vault PDA — not the owner. That
/// single choice is the lock: after this instruction the owner can only move
/// the tokens back out through this program's own `withdraw`, and only while
/// the switch has not tripped.
#[derive(Accounts)]
pub struct DepositSpl<'info> {
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
        associated_token::authority = owner,
        associated_token::token_program = token_program,
    )]
    pub owner_token_account: InterfaceAccount<'info, TokenAccount>,

    #[account(
        init_if_needed,
        payer = owner,
        associated_token::mint = mint,
        associated_token::authority = vault,
        associated_token::token_program = token_program,
    )]
    pub vault_token_account: InterfaceAccount<'info, TokenAccount>,

    pub token_program: Interface<'info, TokenInterface>,
    pub associated_token_program: Program<'info, AssociatedToken>,
    pub system_program: Program<'info, System>,
}

pub fn handle_deposit_spl(ctx: Context<DepositSpl>, amount: u64) -> Result<()> {
    require!(amount > 0, DmsError::InvalidAmount);

    // `transfer_checked` rather than `transfer`: it re-verifies mint and
    // decimals on-chain, and it is the only variant Token-2022 accepts.
    let cpi_accounts = TransferChecked {
        from: ctx.accounts.owner_token_account.to_account_info(),
        mint: ctx.accounts.mint.to_account_info(),
        to: ctx.accounts.vault_token_account.to_account_info(),
        authority: ctx.accounts.owner.to_account_info(),
    };
    transfer_checked(
        CpiContext::new(ctx.accounts.token_program.key(), cpi_accounts),
        amount,
        ctx.accounts.mint.decimals,
    )?;

    msg!(
        "deposited {} tokens of mint {} into vault {}",
        amount,
        ctx.accounts.mint.key(),
        ctx.accounts.vault.key()
    );

    Ok(())
}
