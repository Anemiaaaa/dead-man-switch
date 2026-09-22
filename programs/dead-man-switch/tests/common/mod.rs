//! Shared harness for the LiteSVM integration tests.
//!
//! Every test boots a fresh in-process SVM with the compiled program loaded,
//! so a test run needs `anchor build` first but no validator and no network.

#![allow(dead_code)]
// litesvm's `FailedTransactionMetadata` is a few hundred bytes and it is the
// error type its own API returns. Boxing it here would only add a layer
// between every test and the logs it asserts on.
#![allow(clippy::result_large_err)]

use anchor_lang::{
    error::ERROR_CODE_OFFSET,
    prelude::Pubkey,
    solana_program::{instruction::Instruction, system_program},
    AccountDeserialize, InstructionData, ToAccountMetas,
};
use anchor_spl::associated_token::get_associated_token_address_with_program_id;
use dead_man_switch::{
    constants::VAULT_SEED,
    error::DmsError,
    state::{BeneficiaryInput, Vault},
};
use litesvm::{
    types::{FailedTransactionMetadata, TransactionMetadata},
    LiteSVM,
};
use litesvm_token::{
    get_spl_account, spl_token::state::Account as SplTokenAccount, CreateAssociatedTokenAccount,
    CreateMint, MintTo,
};
use solana_clock::Clock;
use solana_keypair::Keypair;
use solana_message::{Message, VersionedMessage};
use solana_signer::Signer;
use solana_transaction::versioned::VersionedTransaction;

pub const ONE_SOL: u64 = 1_000_000_000;
pub const HOUR: i64 = 3_600;
pub const DAY: i64 = 24 * HOUR;

/// Wall clock a test SVM starts at.
///
/// LiteSVM boots with `unix_timestamp` at zero, which would date every vault
/// to 1970 and make timeout arithmetic read strangely. Any plausible value
/// does; this one is early 2027.
pub const GENESIS: i64 = 1_800_000_000;

pub type TxResult = Result<TransactionMetadata, FailedTransactionMetadata>;

pub fn program_id() -> Pubkey {
    dead_man_switch::id()
}

/// Fresh SVM plus a funded owner wallet.
pub fn setup() -> (LiteSVM, Keypair) {
    let mut svm = LiteSVM::new();
    let bytes = include_bytes!(concat!(
        env!("CARGO_TARGET_TMPDIR"),
        "/../deploy/dead_man_switch.so"
    ));
    svm.add_program(program_id(), bytes).unwrap();
    set_clock(&mut svm, GENESIS);

    let owner = funded_wallet(&mut svm);
    (svm, owner)
}

/// Current on-chain time as the program will see it.
pub fn now(svm: &LiteSVM) -> i64 {
    svm.get_sysvar::<Clock>().unix_timestamp
}

pub fn set_clock(svm: &mut LiteSVM, unix_timestamp: i64) {
    let mut clock: Clock = svm.get_sysvar();
    clock.unix_timestamp = unix_timestamp;
    svm.set_sysvar(&clock);
}

/// Moves time forward — how the tests let a timeout elapse.
pub fn advance_clock(svm: &mut LiteSVM, seconds: i64) {
    let target = now(svm) + seconds;
    set_clock(svm, target);
}

/// A wallet with enough SOL that rent and fees never skew an assertion.
pub fn funded_wallet(svm: &mut LiteSVM) -> Keypair {
    let wallet = Keypair::new();
    svm.airdrop(&wallet.pubkey(), 1_000 * ONE_SOL).unwrap();
    wallet
}

pub fn vault_pda(owner: &Pubkey, vault_id: u64) -> Pubkey {
    Pubkey::find_program_address(
        &[VAULT_SEED, owner.as_ref(), &vault_id.to_le_bytes()],
        &program_id(),
    )
    .0
}

pub fn heirs(entries: &[(Pubkey, u16)]) -> Vec<BeneficiaryInput> {
    entries
        .iter()
        .map(|(address, share_bps)| BeneficiaryInput {
            address: *address,
            share_bps: *share_bps,
        })
        .collect()
}

pub fn send(svm: &mut LiteSVM, payer: &Keypair, instruction: Instruction) -> TxResult {
    // Repeating an identical instruction in the same test would otherwise
    // produce a byte-identical transaction and be rejected as already
    // processed. A fresh blockhash is what a real client would send anyway.
    svm.expire_blockhash();
    let blockhash = svm.latest_blockhash();
    let message = Message::new_with_blockhash(&[instruction], Some(&payer.pubkey()), &blockhash);
    let transaction =
        VersionedTransaction::try_new(VersionedMessage::Legacy(message), &[payer]).unwrap();
    svm.send_transaction(transaction)
}

pub fn read_vault(svm: &LiteSVM, vault: &Pubkey) -> Vault {
    let account = svm
        .get_account(vault)
        .expect("vault account does not exist");
    Vault::try_deserialize(&mut account.data.as_slice()).expect("vault did not deserialize")
}

/// Asserts the transaction failed with a specific Anchor error.
///
/// Matches on the error number Anchor logs, which survives across
/// `solana-*` crate version bumps better than the `TransactionError` shape.
pub fn assert_error(result: TxResult, expected: DmsError) {
    let failure = match result {
        Err(failure) => failure,
        Ok(_) => panic!("expected {expected:?}, but the transaction succeeded"),
    };

    let needle = format!("Error Number: {}", expected as u32 + ERROR_CODE_OFFSET);
    assert!(
        failure.meta.logs.iter().any(|line| line.contains(&needle)),
        "expected {expected:?} ({needle}), got:\n{}",
        failure.meta.logs.join("\n")
    );
}

// --- instruction builders -------------------------------------------------

pub fn initialize_vault_ix(
    owner: &Pubkey,
    vault_id: u64,
    timeout_seconds: i64,
    beneficiaries: Vec<BeneficiaryInput>,
    mint: Option<Pubkey>,
) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::InitializeVault {
            vault_id,
            timeout_seconds,
            beneficiaries,
        }
        .data(),
        dead_man_switch::accounts::InitializeVault {
            owner: *owner,
            vault: vault_pda(owner, vault_id),
            mint,
            system_program: system_program::ID,
        }
        .to_account_metas(None),
    )
}

pub fn deposit_sol_ix(owner: &Pubkey, vault: &Pubkey, amount: u64) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::DepositSol { amount }.data(),
        dead_man_switch::accounts::DepositSol {
            owner: *owner,
            vault: *vault,
            system_program: system_program::ID,
        }
        .to_account_metas(None),
    )
}

pub fn deposit_spl_ix(owner: &Pubkey, vault: &Pubkey, mint: &Pubkey, amount: u64) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::DepositSpl { amount }.data(),
        dead_man_switch::accounts::DepositSpl {
            owner: *owner,
            vault: *vault,
            mint: *mint,
            owner_token_account: ata(owner, mint),
            vault_token_account: ata(vault, mint),
            token_program: anchor_spl::token::ID,
            associated_token_program: anchor_spl::associated_token::ID,
            system_program: system_program::ID,
        }
        .to_account_metas(None),
    )
}

pub fn check_in_ix(owner: &Pubkey, vault: &Pubkey) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::CheckIn {}.data(),
        dead_man_switch::accounts::CheckIn {
            owner: *owner,
            vault: *vault,
        }
        .to_account_metas(None),
    )
}

pub fn set_beneficiaries_ix(
    owner: &Pubkey,
    vault: &Pubkey,
    beneficiaries: Vec<BeneficiaryInput>,
) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::SetBeneficiaries { beneficiaries }.data(),
        dead_man_switch::accounts::SetBeneficiaries {
            owner: *owner,
            vault: *vault,
        }
        .to_account_metas(None),
    )
}

pub fn withdraw_sol_ix(owner: &Pubkey, vault: &Pubkey, amount: u64) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::WithdrawSol { amount }.data(),
        dead_man_switch::accounts::WithdrawSol {
            owner: *owner,
            vault: *vault,
        }
        .to_account_metas(None),
    )
}

pub fn withdraw_spl_ix(owner: &Pubkey, vault: &Pubkey, mint: &Pubkey, amount: u64) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::WithdrawSpl { amount }.data(),
        dead_man_switch::accounts::WithdrawSpl {
            owner: *owner,
            vault: *vault,
            mint: *mint,
            vault_token_account: ata(vault, mint),
            owner_token_account: ata(owner, mint),
            token_program: anchor_spl::token::ID,
            associated_token_program: anchor_spl::associated_token::ID,
            system_program: system_program::ID,
        }
        .to_account_metas(None),
    )
}

pub fn claim_sol_ix(beneficiary: &Pubkey, vault: &Pubkey) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::ClaimSol {}.data(),
        dead_man_switch::accounts::ClaimSol {
            beneficiary: *beneficiary,
            vault: *vault,
        }
        .to_account_metas(None),
    )
}

pub fn claim_spl_ix(beneficiary: &Pubkey, vault: &Pubkey, mint: &Pubkey) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::ClaimSpl {}.data(),
        dead_man_switch::accounts::ClaimSpl {
            beneficiary: *beneficiary,
            vault: *vault,
            mint: *mint,
            vault_token_account: ata(vault, mint),
            beneficiary_token_account: ata(beneficiary, mint),
            token_program: anchor_spl::token::ID,
            associated_token_program: anchor_spl::associated_token::ID,
            system_program: system_program::ID,
        }
        .to_account_metas(None),
    )
}

/// `mint` is `Some` for an SPL vault, which also pulls in the token account
/// and the token program.
pub fn close_vault_ix(owner: &Pubkey, vault: &Pubkey, mint: Option<Pubkey>) -> Instruction {
    Instruction::new_with_bytes(
        program_id(),
        &dead_man_switch::instruction::CloseVault {}.data(),
        dead_man_switch::accounts::CloseVault {
            owner: *owner,
            vault: *vault,
            vault_token_account: mint.map(|mint| ata(vault, &mint)),
            token_program: mint.map(|_| anchor_spl::token::ID),
        }
        .to_account_metas(None),
    )
}

/// Associated token account address for `wallet` under the classic SPL Token
/// program — the one `litesvm-token` creates mints under by default.
pub fn ata(wallet: &Pubkey, mint: &Pubkey) -> Pubkey {
    get_associated_token_address_with_program_id(wallet, mint, &anchor_spl::token::ID)
}

pub fn token_balance(svm: &LiteSVM, account: &Pubkey) -> u64 {
    get_spl_account::<SplTokenAccount>(svm, account)
        .expect("token account not found")
        .amount
}

/// Opens a native SOL vault with a single heir taking everything.
pub fn open_sol_vault(
    svm: &mut LiteSVM,
    owner: &Keypair,
    vault_id: u64,
    timeout_seconds: i64,
) -> (Pubkey, Keypair) {
    let heir = Keypair::new();
    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        vault_id,
        timeout_seconds,
        heirs(&[(heir.pubkey(), 10_000)]),
        None,
    );
    send(svm, owner, instruction).expect("opening the vault failed");
    (vault_pda(&owner.pubkey(), vault_id), heir)
}

/// Lamports in the vault that are not pinned down by rent-exemption — the
/// part heirs and the owner can actually move.
pub fn vault_available(svm: &LiteSVM, vault: &Pubkey) -> u64 {
    let account = svm.get_account(vault).expect("vault account not found");
    account.lamports - svm.minimum_balance_for_rent_exemption(account.data.len())
}

pub fn lamports(svm: &LiteSVM, key: &Pubkey) -> u64 {
    svm.get_account(key).expect("account not found").lamports
}

/// A vault plus the funded heir wallets it names.
pub struct VaultFixture {
    pub vault: Pubkey,
    pub heirs: Vec<Keypair>,
}

/// Opens a vault with one funded heir wallet per entry in `shares`.
///
/// Heirs are funded because claiming costs them a transaction fee — an heir
/// with a zero balance could not collect their own inheritance.
pub fn open_vault_with(
    svm: &mut LiteSVM,
    owner: &Keypair,
    vault_id: u64,
    timeout_seconds: i64,
    shares: &[u16],
    mint: Option<Pubkey>,
) -> VaultFixture {
    let heir_wallets: Vec<Keypair> = shares.iter().map(|_| funded_wallet(svm)).collect();
    let entries: Vec<(Pubkey, u16)> = heir_wallets
        .iter()
        .zip(shares)
        .map(|(wallet, share)| (wallet.pubkey(), *share))
        .collect();

    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        vault_id,
        timeout_seconds,
        heirs(&entries),
        mint,
    );
    send(svm, owner, instruction).expect("opening the vault failed");

    VaultFixture {
        vault: vault_pda(&owner.pubkey(), vault_id),
        heirs: heir_wallets,
    }
}

/// A mint plus a funded owner token account, ready to deposit from.
pub struct SplFixture {
    pub mint: Pubkey,
    pub owner_token_account: Pubkey,
}

pub fn mint_with_balance(svm: &mut LiteSVM, owner: &Keypair, amount: u64) -> SplFixture {
    let mint = CreateMint::new(svm, owner)
        .decimals(6)
        .send()
        .expect("creating the mint failed");
    let owner_token_account = CreateAssociatedTokenAccount::new(svm, owner, &mint)
        .send()
        .expect("creating the owner ATA failed");
    MintTo::new(svm, owner, &mint, &owner_token_account, amount)
        .send()
        .expect("minting failed");

    SplFixture {
        mint,
        owner_token_account,
    }
}

/// Creates `wallet`'s ATA and leaves it empty.
///
/// Anyone may create an associated token account for anyone else, so a test
/// that wants to reach a guard rather than trip over a missing account has to
/// put the account there first.
pub fn create_token_account(
    svm: &mut LiteSVM,
    payer: &Keypair,
    mint: &Pubkey,
    wallet: &Pubkey,
) -> Pubkey {
    CreateAssociatedTokenAccount::new(svm, payer, mint)
        .owner(wallet)
        .send()
        .expect("creating the ATA failed")
}

/// Creates `wallet`'s ATA and mints `amount` into it.
///
/// `mint_authority` must be the keypair that created the mint.
pub fn fund_token_account(
    svm: &mut LiteSVM,
    mint_authority: &Keypair,
    mint: &Pubkey,
    wallet: &Pubkey,
    amount: u64,
) -> Pubkey {
    let account = CreateAssociatedTokenAccount::new(svm, mint_authority, mint)
        .owner(wallet)
        .send()
        .expect("creating the ATA failed");
    MintTo::new(svm, mint_authority, mint, &account, amount)
        .send()
        .expect("minting failed");
    account
}

/// Opens an SPL vault with a single heir taking everything.
pub fn open_spl_vault(
    svm: &mut LiteSVM,
    owner: &Keypair,
    vault_id: u64,
    timeout_seconds: i64,
    mint: &Pubkey,
) -> (Pubkey, Keypair) {
    let heir = Keypair::new();
    let instruction = initialize_vault_ix(
        &owner.pubkey(),
        vault_id,
        timeout_seconds,
        heirs(&[(heir.pubkey(), 10_000)]),
        Some(*mint),
    );
    send(svm, owner, instruction).expect("opening the SPL vault failed");
    (vault_pda(&owner.pubkey(), vault_id), heir)
}
