use trident_fuzz::fuzzing::*;

/// Storage for all account addresses used in fuzz testing.
///
/// This struct serves as a centralized repository for account addresses,
/// enabling their reuse across different instruction flows and test scenarios.
///
/// Docs: https://ackee.xyz/trident/docs/latest/trident-api-macro/trident-types/fuzz-accounts/
#[derive(Default)]
pub struct AccountAddresses {
    pub owner: AddressStorage,

    pub vault: AddressStorage,

    pub beneficiary: AddressStorage,

    pub mint: AddressStorage,

    pub vault_token_account: AddressStorage,

    pub beneficiary_token_account: AddressStorage,

    pub token_program: AddressStorage,

    pub associated_token_program: AddressStorage,

    pub system_program: AddressStorage,

    pub owner_token_account: AddressStorage,

    /// Wallets with no claim on the vault, used by the hostile flow. Not
    /// derived from the IDL — nothing in the program's account list is named
    /// for an attacker, which is rather the point.
    pub stranger: AddressStorage,
}
