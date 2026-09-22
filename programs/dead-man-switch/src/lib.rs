//! # Dead Man's Switch
//!
//! On-chain digital inheritance for Solana. An owner locks SOL or SPL tokens
//! in a vault and periodically checks in. Miss enough check-ins and the listed
//! heirs may take their shares — no oracle, no custodian, no court.
//!
//! `claim` is permissionless by design: any listed heir triggers their own
//! payout as soon as the timer runs out, so the protocol keeps working even if
//! the off-chain keeper service is down.

pub mod constants;
pub mod error;
pub mod instructions;
pub mod state;
pub mod utils;

use anchor_lang::prelude::*;

pub use constants::*;
pub use instructions::*;
pub use state::*;

declare_id!("9tfSr7zg9bGBfpSqqdwCACiSfAqsbdE4rNnwezFe5Ldm");

#[program]
pub mod dead_man_switch {
    use super::*;

    /// Open a vault: fix the timer, the heir table and the asset it holds.
    ///
    /// Pass a `mint` account for an SPL vault, or omit it for native SOL.
    pub fn initialize_vault(
        ctx: Context<InitializeVault>,
        vault_id: u64,
        timeout_seconds: i64,
        beneficiaries: Vec<BeneficiaryInput>,
    ) -> Result<()> {
        instructions::initialize_vault::handle_initialize_vault(
            ctx,
            vault_id,
            timeout_seconds,
            beneficiaries,
        )
    }

    /// Move lamports from the owner into a native SOL vault.
    pub fn deposit_sol(ctx: Context<DepositSol>, amount: u64) -> Result<()> {
        instructions::deposit_sol::handle_deposit_sol(ctx, amount)
    }

    /// Move SPL tokens from the owner into the vault's token account.
    pub fn deposit_spl(ctx: Context<DepositSpl>, amount: u64) -> Result<()> {
        instructions::deposit_spl::handle_deposit_spl(ctx, amount)
    }

    /// "I'm alive" — reset the timer.
    pub fn check_in(ctx: Context<CheckIn>) -> Result<()> {
        instructions::check_in::handle_check_in(ctx)
    }

    /// Replace the heir table, shares and all.
    pub fn set_beneficiaries(
        ctx: Context<SetBeneficiaries>,
        beneficiaries: Vec<BeneficiaryInput>,
    ) -> Result<()> {
        instructions::set_beneficiaries::handle_set_beneficiaries(ctx, beneficiaries)
    }

    /// Take lamports back out while the switch is still armed.
    pub fn withdraw_sol(ctx: Context<WithdrawSol>, amount: u64) -> Result<()> {
        instructions::withdraw_sol::handle_withdraw_sol(ctx, amount)
    }

    /// Take SPL tokens back out while the switch is still armed.
    pub fn withdraw_spl(ctx: Context<WithdrawSpl>, amount: u64) -> Result<()> {
        instructions::withdraw_spl::handle_withdraw_spl(ctx, amount)
    }

    /// An heir takes their share of a native SOL vault.
    pub fn claim_sol(ctx: Context<ClaimSol>) -> Result<()> {
        instructions::claim_sol::handle_claim_sol(ctx)
    }

    /// An heir takes their share of an SPL token vault.
    pub fn claim_spl(ctx: Context<ClaimSpl>) -> Result<()> {
        instructions::claim_spl::handle_claim_spl(ctx)
    }

    /// Close an empty vault and reclaim its rent.
    pub fn close_vault(ctx: Context<CloseVault>) -> Result<()> {
        instructions::close_vault::handle_close_vault(ctx)
    }
}
