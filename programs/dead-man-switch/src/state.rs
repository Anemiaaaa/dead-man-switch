use anchor_lang::prelude::*;

use crate::{
    constants::{MAX_BENEFICIARIES, TOTAL_SHARE_BPS},
    error::DmsError,
};

/// One heir slot inside a [`Vault`].
///
/// Slots live in a fixed-size array, so an unused slot is just
/// `Beneficiary::default()`: zeroed address, zero share. Only the first
/// [`Vault::beneficiary_count`] slots are meaningful.
#[derive(
    AnchorSerialize, AnchorDeserialize, InitSpace, Clone, Copy, Debug, Default, PartialEq, Eq,
)]
pub struct Beneficiary {
    /// Wallet allowed to call `claim` once the switch has tripped.
    pub address: Pubkey,
    /// Share of the vault in basis points; active slots sum to 10_000.
    pub share_bps: u16,
    /// Whether this heir has already taken their share.
    ///
    /// Tracked per heir, not per vault, because each heir claims on their own
    /// — nobody has to wait for the others to show up.
    pub claimed: bool,
}

/// What the client passes in when creating or amending a vault.
///
/// Deliberately narrower than [`Beneficiary`]: `claimed` is program state and
/// must never be settable from outside.
#[derive(AnchorSerialize, AnchorDeserialize, Clone, Copy, Debug, PartialEq, Eq)]
pub struct BeneficiaryInput {
    pub address: Pubkey,
    pub share_bps: u16,
}

/// The switch itself: one vault, one owner, one timer, up to five heirs.
///
/// PDA seeds are `["vault", owner, vault_id]`, so one wallet can run several
/// independent vaults (different assets, different heirs, different timers).
#[account]
#[derive(InitSpace, Debug)]
pub struct Vault {
    /// Wallet that funds the vault, checks in, and may withdraw while alive.
    pub owner: Pubkey,

    /// Seed salt, letting one owner hold several vaults.
    pub vault_id: u64,

    /// `None` for a native SOL vault, `Some(mint)` for an SPL token vault.
    pub mint: Option<Pubkey>,

    /// Unix timestamp of the owner's last "I'm alive" signal.
    pub last_checkin: i64,

    /// How long the owner may stay silent before heirs can claim.
    pub timeout_seconds: i64,

    /// Fixed-size heir table; see [`Vault::active`].
    pub beneficiaries: [Beneficiary; MAX_BENEFICIARIES],

    /// How many slots of `beneficiaries` are in use.
    pub beneficiary_count: u8,

    /// Set on the first successful `claim` — the moment the switch trips.
    ///
    /// From then on the vault is frozen for the owner: no check-in, no
    /// deposit, no edits to the heir table.
    pub is_claimed: bool,

    /// Total amount the shares are divided over, snapshotted on the first
    /// `claim`; zero until the switch trips.
    ///
    /// Without this snapshot the split would be wrong: the second heir would
    /// take their percentage of whatever the first heir left behind, not of
    /// what the vault actually held.
    pub claim_pool: u64,

    /// Bump of the vault PDA.
    pub bump: u8,
}

impl Vault {
    /// The heir slots that are actually in use.
    pub fn active(&self) -> &[Beneficiary] {
        &self.beneficiaries[..self.beneficiary_count as usize]
    }

    /// Mutable view of the heir slots that are in use.
    pub fn active_mut(&mut self) -> &mut [Beneficiary] {
        let count = self.beneficiary_count as usize;
        &mut self.beneficiaries[..count]
    }

    /// Timestamp from which heirs may claim.
    pub fn deadline(&self) -> Result<i64> {
        self.last_checkin
            .checked_add(self.timeout_seconds)
            .ok_or_else(|| error!(DmsError::MathOverflow))
    }

    /// Whether the switch has tripped as of `now`.
    ///
    /// Callers must pass a freshly read `Clock`, never a cached value — see
    /// the security invariants in the README.
    pub fn is_expired(&self, now: i64) -> Result<bool> {
        Ok(now >= self.deadline()?)
    }

    /// Index of `address` in the heir table, if it is listed.
    pub fn index_of(&self, address: &Pubkey) -> Option<usize> {
        self.active().iter().position(|b| b.address == *address)
    }

    /// True for a native SOL vault.
    pub fn is_sol(&self) -> bool {
        self.mint.is_none()
    }

    /// Validates a candidate heir table: non-empty, no duplicates, no zero
    /// shares, shares summing to exactly 100%.
    pub fn validate_slots(slots: &[Beneficiary]) -> Result<()> {
        require!(!slots.is_empty(), DmsError::NoBeneficiaries);
        require!(
            slots.len() <= MAX_BENEFICIARIES,
            DmsError::TooManyBeneficiaries
        );

        let mut total: u16 = 0;
        for (i, slot) in slots.iter().enumerate() {
            require_keys_neq!(
                slot.address,
                Pubkey::default(),
                DmsError::InvalidBeneficiary
            );
            require!(slot.share_bps > 0, DmsError::InvalidShares);
            require!(
                !slots[..i].iter().any(|prev| prev.address == slot.address),
                DmsError::DuplicateBeneficiary
            );
            total = total
                .checked_add(slot.share_bps)
                .ok_or_else(|| error!(DmsError::MathOverflow))?;
        }
        require_eq!(total, TOTAL_SHARE_BPS, DmsError::InvalidShares);

        Ok(())
    }

    /// Lamports held by the vault account beyond its rent-exempt reserve.
    ///
    /// This is the part heirs and the owner may move; touching the reserve
    /// would put the account at risk of being purged mid-flight.
    pub fn withdrawable_lamports(vault_info: &AccountInfo) -> Result<u64> {
        let reserve = Rent::get()?.minimum_balance(vault_info.data_len());
        Ok(vault_info.lamports().saturating_sub(reserve))
    }

    /// `pool * share_bps / 10_000`, computed in `u128` so a large pool cannot
    /// overflow the multiplication.
    pub fn share_of(pool: u64, share_bps: u16) -> Result<u64> {
        let amount = (pool as u128)
            .checked_mul(share_bps as u128)
            .and_then(|v| v.checked_div(TOTAL_SHARE_BPS as u128))
            .ok_or_else(|| error!(DmsError::MathOverflow))?;

        u64::try_from(amount).map_err(|_| error!(DmsError::MathOverflow))
    }

    /// Checks that `claimant` may take their share right now, and books it.
    ///
    /// `pool` is what the vault currently holds, and is only used on the very
    /// first claim — from then on every heir is measured against that same
    /// snapshot, which is the whole point of [`Vault::claim_pool`].
    ///
    /// Asset-agnostic on purpose: the SOL and SPL paths differ only in how
    /// they read `pool` and how they move the funds, so the rules about who
    /// may claim what live here, once.
    pub fn authorize_claim(
        &mut self,
        claimant: &Pubkey,
        now: i64,
        pool: u64,
    ) -> Result<ClaimTicket> {
        require!(self.is_expired(now)?, DmsError::TimeoutNotReached);

        let index = self
            .index_of(claimant)
            .ok_or_else(|| error!(DmsError::Unauthorized))?;
        require!(!self.beneficiaries[index].claimed, DmsError::AlreadyClaimed);

        if !self.is_claimed {
            self.is_claimed = true;
            self.claim_pool = pool;
        }

        // Counted before this heir is marked, so `1` means they are the last
        // one standing.
        let is_last = self.active().iter().filter(|b| !b.claimed).count() == 1;
        let share_bps = self.beneficiaries[index].share_bps;
        self.beneficiaries[index].claimed = true;

        Ok(ClaimTicket { share_bps, is_last })
    }
}

/// The outcome of [`Vault::authorize_claim`]: what this heir is owed, and
/// whether anyone is left after them.
#[derive(Clone, Copy, Debug)]
pub struct ClaimTicket {
    pub share_bps: u16,
    /// True when no unclaimed heir remains.
    ///
    /// The last heir sweeps whatever is left rather than taking a computed
    /// share, so integer division cannot strand dust in a vault nobody can
    /// reopen.
    pub is_last: bool,
}
