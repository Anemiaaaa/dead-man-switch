use anchor_lang::prelude::*;

/// Seed prefix of the vault PDA: `["vault", owner, vault_id]`.
#[constant]
pub const VAULT_SEED: &[u8] = b"vault";

/// Upper bound on heirs per vault.
///
/// The list is a fixed-size array rather than a `Vec` so the account size is
/// known at `init` time: no `realloc`, no rent top-ups, one less moving part
/// to get wrong.
pub const MAX_BENEFICIARIES: usize = 5;

/// Shares of all active heirs must add up to exactly 100%.
#[constant]
pub const TOTAL_SHARE_BPS: u16 = 10_000;

/// Shortest timer we accept, in seconds.
///
/// Guards the owner against a vault that is claimable the moment it is funded;
/// one minute is still short enough to demo the full lifecycle.
#[constant]
pub const MIN_TIMEOUT_SECONDS: i64 = 60;

/// Longest timer we accept: 10 years.
///
/// Keeps `last_checkin + timeout_seconds` far away from `i64` overflow and
/// rules out a timer so long the vault is effectively a black hole.
#[constant]
pub const MAX_TIMEOUT_SECONDS: i64 = 10 * 365 * 24 * 60 * 60;
