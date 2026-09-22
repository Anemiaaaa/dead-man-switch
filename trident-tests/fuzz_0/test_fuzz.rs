//! Fuzzing the switch with [Trident](https://ackee.xyz/trident).
//!
//! The LiteSVM tests check sequences somebody thought of. This checks the ones
//! nobody did: random interleavings of deposits, withdrawals, check-ins,
//! claims and — crucially — random jumps forward in time, which is the one
//! input this protocol is actually built around.
//!
//! The properties below are asserted after *every* action, so a violation is
//! reported with the seed that produced it rather than as a mystery.

use fuzz_accounts::*;
use trident_fuzz::fuzzing::*;
mod fuzz_accounts;
mod types;
use types::*;

use crate::types::dead_man_switch::{
    BeneficiaryInput, CheckInInstruction, CheckInInstructionAccounts, CheckInInstructionData,
    ClaimSolInstruction,
    ClaimSolInstructionAccounts, ClaimSolInstructionData, DepositSolInstruction,
    DepositSolInstructionAccounts, DepositSolInstructionData, InitializeVaultInstruction,
    InitializeVaultInstructionAccounts, InitializeVaultInstructionData, Vault,
    WithdrawSolInstruction, WithdrawSolInstructionAccounts, WithdrawSolInstructionData,
};

/// Seed salt for the one vault each iteration works on.
const VAULT_ID: u64 = 1;

/// Shares of the three heirs, in basis points. Deliberately not divisible into
/// round numbers, so integer division has something to get wrong.
const SHARES: [u16; 3] = [5_000, 3_333, 1_667];

const TOTAL_SHARE_BPS: u64 = 10_000;

#[derive(FuzzTestMethods)]
struct FuzzTest {
    /// Trident client for interacting with the Solana program
    trident: Trident,
    /// Storage for all account addresses used in fuzz testing
    fuzz_accounts: AccountAddresses,
}

#[flow_executor]
impl FuzzTest {
    fn new() -> Self {
        Self {
            trident: Trident::default(),
            fuzz_accounts: AccountAddresses::default(),
        }
    }

    /// Opens one funded vault with three heirs. Every flow below acts on it.
    #[init]
    fn start(&mut self) {
        let owner = self.fuzz_accounts.owner.insert(&mut self.trident, None);
        self.trident.airdrop(&owner, 1_000 * LAMPORTS_PER_SOL);

        // The heirs share one storage slot; `get` then picks between them,
        // which is what makes "a random heir tries something" a flow.
        let mut heirs = Vec::with_capacity(SHARES.len());
        for _ in 0..SHARES.len() {
            let heir = self
                .fuzz_accounts
                .beneficiary
                .insert(&mut self.trident, None);
            self.trident.airdrop(&heir, 10 * LAMPORTS_PER_SOL);
            heirs.push(heir);
        }

        let owner_seed = owner.to_bytes();
        let id_seed = VAULT_ID.to_le_bytes();
        let vault = self.fuzz_accounts.vault.insert(
            &mut self.trident,
            Some(PdaSeeds {
                seeds: &[b"vault", &owner_seed, &id_seed],
                program_id: dead_man_switch::program_id(),
            }),
        );

        let table: Vec<BeneficiaryInput> = heirs
            .iter()
            .zip(SHARES)
            .map(|(address, share_bps)| BeneficiaryInput {
                address: *address,
                share_bps,
            })
            .collect();

        // A short timer, so the fuzzer's time jumps straddle the deadline
        // often rather than almost never.
        let timeout = self.trident.random_from_range(60i64..3_600i64);

        let ix = InitializeVaultInstruction::data(InitializeVaultInstructionData::new(
            VAULT_ID, timeout, table,
        ))
        .accounts(InitializeVaultInstructionAccounts::new(
            owner,
            vault,
            // Anchor's wire form for an omitted optional account: a SOL vault.
            dead_man_switch::program_id(),
        ))
        .instruction();

        if !self
            .trident
            .process_transaction(&[ix], Some("initialize_vault"))
            .is_success()
        {
            return;
        }

        let amount = self
            .trident
            .random_from_range(1u64..50 * LAMPORTS_PER_SOL);
        let ix = DepositSolInstruction::data(DepositSolInstructionData::new(amount))
            .accounts(DepositSolInstructionAccounts::new(owner, vault))
            .instruction();
        self.trident
            .process_transaction(&[ix], Some("deposit_sol"));

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// The owner puts more in.
    #[flow]
    fn owner_deposits(&mut self) {
        let Some((owner, vault)) = owner_and_vault(&mut self.trident, &mut self.fuzz_accounts)
        else {
            return;
        };

        let amount = self.trident.random_from_range(0u64..20 * LAMPORTS_PER_SOL);
        let ix = DepositSolInstruction::data(DepositSolInstructionData::new(amount))
            .accounts(DepositSolInstructionAccounts::new(owner, vault))
            .instruction();
        self.trident
            .process_transaction(&[ix], Some("deposit_sol"));

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// The owner takes some back — including amounts that should be refused.
    #[flow]
    fn owner_withdraws(&mut self) {
        let Some((owner, vault)) = owner_and_vault(&mut self.trident, &mut self.fuzz_accounts)
        else {
            return;
        };

        // Deliberately unbounded by the balance: asking for more than the
        // vault holds has to fail cleanly rather than underflow.
        let amount = self.trident.random_from_range(0u64..80 * LAMPORTS_PER_SOL);
        let ix = WithdrawSolInstruction::data(WithdrawSolInstructionData::new(amount))
            .accounts(WithdrawSolInstructionAccounts::new(owner, vault))
            .instruction();
        self.trident
            .process_transaction(&[ix], Some("withdraw_sol"));

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// The owner says they are alive.
    #[flow]
    fn owner_checks_in(&mut self) {
        let Some((owner, vault)) = owner_and_vault(&mut self.trident, &mut self.fuzz_accounts)
        else {
            return;
        };

        let before = read_vault(&mut self.trident, &vault);

        let ix = CheckInInstruction::data(CheckInInstructionData::new())
            .accounts(CheckInInstructionAccounts::new(owner, vault))
            .instruction();
        let res = self.trident.process_transaction(&[ix], Some("check_in"));

        if let (true, Some(before), Some(after)) =
            (res.is_success(), before, read_vault(&mut self.trident, &vault))
        {
            assert!(
                after.last_checkin >= before.last_checkin,
                "a successful check-in moved the deadline backwards: {} -> {}",
                before.last_checkin,
                after.last_checkin
            );
            assert!(
                !before.is_claimed,
                "check-in succeeded on a vault that had already been claimed"
            );
        }

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// Time moves. This is the input the whole protocol turns on, so it gets
    /// its own flow and a wide range — sometimes not enough to matter,
    /// sometimes far past the deadline.
    #[flow]
    fn time_passes(&mut self) {
        let seconds = self.trident.random_from_range(1i64..7_200i64);
        self.trident.forward_in_time(seconds);

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// A random heir tries to take their share.
    #[flow]
    fn heir_claims(&mut self) {
        let Some(vault) = self.fuzz_accounts.vault.get(&mut self.trident) else {
            return;
        };
        let Some(heir) = self.fuzz_accounts.beneficiary.get(&mut self.trident) else {
            return;
        };

        let before = read_vault(&mut self.trident, &vault);

        let ix = ClaimSolInstruction::data(ClaimSolInstructionData::new())
            .accounts(ClaimSolInstructionAccounts::new(heir, vault))
            .instruction();
        let res = self.trident.process_transaction(&[ix], Some("claim_sol"));

        if res.is_success() {
            let before = before.expect("a claim succeeded against a vault that did not exist");

            // The deadline had to have passed, measured against the state as
            // it was before this transaction.
            let deadline = before.last_checkin + before.timeout_seconds;
            assert!(
                res.transaction_timestamp() >= deadline,
                "a claim succeeded {} seconds before the deadline",
                deadline - res.transaction_timestamp()
            );

            // And the claimant had to be a listed heir who had not been paid.
            let slot = before
                .beneficiaries
                .iter()
                .take(before.beneficiary_count as usize)
                .find(|b| b.address == heir)
                .expect("a claim succeeded for a wallet that is not a beneficiary");
            assert!(!slot.claimed, "an heir was paid twice");
        }

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    /// Somebody who owns nothing tries everything. None of it may work.
    #[flow]
    fn stranger_attacks(&mut self) {
        let Some(vault) = self.fuzz_accounts.vault.get(&mut self.trident) else {
            return;
        };

        let stranger = self.fuzz_accounts.stranger.insert(&mut self.trident, None);
        self.trident.airdrop(&stranger, 10 * LAMPORTS_PER_SOL);

        let amount = self.trident.random_from_range(1u64..10 * LAMPORTS_PER_SOL);

        let attempts = [
            (
                "check_in",
                CheckInInstruction::data(CheckInInstructionData::new())
                    .accounts(CheckInInstructionAccounts::new(stranger, vault))
                    .instruction(),
            ),
            (
                "withdraw_sol",
                WithdrawSolInstruction::data(WithdrawSolInstructionData::new(amount))
                    .accounts(WithdrawSolInstructionAccounts::new(stranger, vault))
                    .instruction(),
            ),
            (
                "claim_sol",
                ClaimSolInstruction::data(ClaimSolInstructionData::new())
                    .accounts(ClaimSolInstructionAccounts::new(stranger, vault))
                    .instruction(),
            ),
        ];

        for (name, ix) in attempts {
            let res = self
                .trident
                .process_transaction(&[ix], Some("stranger attempt"));
            assert!(
                !res.is_success(),
                "a wallet with no claim on this vault got {name} to succeed"
            );
        }

        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }

    #[end]
    fn end(&mut self) {
        check_invariants(&mut self.trident, &mut self.fuzz_accounts);
    }
}

// --- properties -------------------------------------------------------------

/// Everything that must be true of the vault no matter what just happened.
///
/// Read entirely from on-chain state, so it holds the program to its own
/// stored words rather than to a model kept alongside it.
fn check_invariants(trident: &mut Trident, accounts: &mut AccountAddresses) {
    let Some(vault_key) = accounts.vault.get(trident) else {
        return;
    };
    let Some(vault) = read_vault(trident, &vault_key) else {
        return;
    };

    let count = vault.beneficiary_count as usize;
    assert!(
        count <= vault.beneficiaries.len(),
        "the heir count ({count}) is past the end of the table"
    );
    let active = &vault.beneficiaries[..count];

    // Shares always add up to exactly one hundred percent.
    let total: u64 = active.iter().map(|b| b.share_bps as u64).sum();
    assert_eq!(
        total, TOTAL_SHARE_BPS,
        "heir shares total {total} basis points"
    );

    // No heir is listed twice, and none is the default pubkey.
    for (i, heir) in active.iter().enumerate() {
        assert_ne!(
            heir.address,
            Pubkey::default(),
            "heir {i} is the default pubkey"
        );
        assert!(
            !active[..i].iter().any(|prev| prev.address == heir.address),
            "heir {i} is listed twice"
        );
        assert!(heir.share_bps > 0, "heir {i} holds a zero share");
    }

    let paid = active.iter().filter(|b| b.claimed).count();

    // The switch either has not tripped — in which case nobody has been paid
    // and there is no pool — or it has, and there is.
    if vault.is_claimed {
        assert!(
            vault.claim_pool > 0,
            "the switch tripped but the pool it is divided over is zero"
        );
    } else {
        assert_eq!(
            vault.claim_pool, 0,
            "a pool was snapshotted before the switch tripped"
        );
        assert_eq!(paid, 0, "{paid} heirs were paid before the switch tripped");
    }

    // The money invariant: once the pool is fixed, the vault must still hold
    // at least what everybody who has not claimed is owed. If a payout ever
    // overpays, this is what catches it.
    if vault.is_claimed {
        let owed: u64 = active
            .iter()
            .filter(|b| !b.claimed)
            .map(|b| vault.claim_pool * b.share_bps as u64 / TOTAL_SHARE_BPS)
            .sum();

        let balance = trident.get_account(&vault_key).lamports();
        assert!(
            balance >= owed,
            "the vault holds {balance} lamports but still owes {owed} to {} unpaid heirs",
            active.len() - paid
        );
    }
}

// --- helpers ----------------------------------------------------------------

fn read_vault(trident: &mut Trident, vault: &Pubkey) -> Option<Vault> {
    trident.get_account_with_type::<Vault>(vault, None)
}

fn owner_and_vault(
    trident: &mut Trident,
    accounts: &mut AccountAddresses,
) -> Option<(Pubkey, Pubkey)> {
    let owner = accounts.owner.get(trident)?;
    let vault = accounts.vault.get(trident)?;
    Some((owner, vault))
}

fn main() {
    FuzzTest::fuzz(1000, 100);
}
