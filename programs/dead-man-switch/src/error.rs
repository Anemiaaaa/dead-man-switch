use anchor_lang::prelude::*;

#[error_code]
pub enum DmsError {
    #[msg("Signer is neither the vault owner nor a listed beneficiary")]
    Unauthorized,

    #[msg("The inactivity timeout has not elapsed yet")]
    TimeoutNotReached,

    #[msg("This beneficiary has already claimed their share")]
    AlreadyClaimed,

    #[msg("Beneficiary shares must add up to exactly 10000 basis points")]
    InvalidShares,

    #[msg("A vault can hold at most 5 beneficiaries")]
    TooManyBeneficiaries,

    #[msg("Arithmetic overflow")]
    MathOverflow,

    #[msg("Timeout must be between 60 seconds and 10 years")]
    InvalidTimeout,

    #[msg("A vault needs at least one beneficiary")]
    NoBeneficiaries,

    #[msg("The same wallet is listed as a beneficiary twice")]
    DuplicateBeneficiary,

    #[msg("Beneficiary address must not be the default pubkey")]
    InvalidBeneficiary,

    #[msg("Amount must be greater than zero")]
    InvalidAmount,

    #[msg("Mint does not match the one this vault was created for")]
    MintMismatch,

    #[msg("This instruction is only valid for a native SOL vault")]
    NotSolVault,

    #[msg("This instruction is only valid for an SPL token vault")]
    NotSplVault,

    #[msg("The switch has already tripped; the owner can no longer change the vault")]
    VaultTriggered,

    #[msg("Vault balance is too low for this operation")]
    InsufficientFunds,

    #[msg("A vault must be empty before it can be closed")]
    VaultNotEmpty,

    #[msg("An SPL vault needs its token account and token program passed in")]
    MissingTokenAccount,
}
